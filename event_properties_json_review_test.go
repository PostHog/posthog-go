package posthog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/posthog/posthog-go/v2/testdata/contextmarshaler"
	"github.com/stretchr/testify/require"
)

type contextJSONProperty = contextmarshaler.Property

func TestCaptureContextMarshalerPrivacy(t *testing.T) {
	customErr := errors.New("context marshaler failed")
	for _, tc := range []struct {
		name, output string
		err          error
		wantFailure  bool
		// Only the non-finite path validates custom output; a normal event keeps one encode pass.
		nonFiniteOnly bool
	}{
		{"valid", `{"redacted":true}`, nil, false, false},
		{"invalid JSON", `{"value":01}`, nil, true, true},
		{"explicit error", "", customErr, true, false},
	} {
		for _, withNonFinite := range []bool{false, true} {
			if tc.nonFiniteOnly && !withNonFinite {
				continue
			}
			t.Run(fmt.Sprintf("%s/nonfinite=%t", tc.name, withNonFinite), func(t *testing.T) {
				input := contextmarshaler.NewProperty("do-not-send", tc.output, tc.err)
				props := Properties{"custom": input, "finite": 42.5, "literal": "NaN"}
				if withNonFinite {
					props["number"] = math.NaN()
				}
				events, _, cb := captureWire(t,
					Capture{Event: "custom", DistinctId: "user", Properties: props},
					Capture{Event: "neighbor", DistinctId: "user", Properties: Properties{"finite": 7}})
				successes, failures := cb.counts()
				if tc.wantFailure {
					require.Equal(t, 1, failures, "invalid custom output must fail instead of exposing exported fields")
					require.Equal(t, 1, successes)
					if tc.err != nil {
						require.ErrorIs(t, cb.failures[0].err, tc.err)
					}
				} else {
					require.Zero(t, failures)
					require.Equal(t, 2, successes)
				}
				require.NotEmpty(t, events)
				encoded, err := json.Marshal(events)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "do-not-send")
				require.NotContains(t, string(encoded), "Credential")
				neighbor := events[len(events)-1]
				require.Equal(t, "neighbor", neighbor["event"])
				require.Equal(t, float64(7), neighbor["properties"].(map[string]interface{})["finite"])
				if !tc.wantFailure {
					got := events[0]["properties"].(map[string]interface{})
					require.Equal(t, map[string]interface{}{"redacted": true}, got["custom"])
					require.Equal(t, 42.5, got["finite"])
					require.Equal(t, "NaN", got["literal"])
					if withNonFinite {
						require.Contains(t, got, "number")
						require.Nil(t, got["number"])
					}
				}
				require.Equal(t, "do-not-send", input.Credential)
				require.Equal(t, input, props["custom"])
			})
		}
	}
}

type countingJSONProperty struct{ calls int }

func (v *countingJSONProperty) MarshalJSON() ([]byte, error) {
	v.calls++
	return []byte(`"opaque"`), nil
}

func TestPrepareCaptureDeepPropertiesMarshalOpaqueValueOnce(t *testing.T) {
	const depth = 64
	custom := &countingJSONProperty{}
	var nested interface{} = map[string]interface{}{"a_custom": custom, "z_number": math.NaN()}
	for i := 0; i < depth; i++ {
		nested = []interface{}{nested}
	}
	got, _ := preparedProperties(t, Capture{Event: "nested", DistinctId: "user", Properties: Properties{"a_number": math.NaN(), "z_nested": nested}})
	// The failed first pass may reach the marshaler before a NaN; normalization adds one call.
	require.Contains(t, []int{1, 2}, custom.calls, "normalization must not repeatedly invoke an opaque marshaler for overlapping subtrees")
	require.Nil(t, got["a_number"])
	value := got["z_nested"]
	for i := 0; i < depth; i++ {
		array, ok := value.([]interface{})
		require.True(t, ok)
		require.Len(t, array, 1)
		value = array[0]
	}
	require.Equal(t, map[string]interface{}{"a_custom": "opaque", "z_number": nil}, value)
}

func BenchmarkPrepareCaptureNonFiniteDepth(b *testing.B) {
	for _, depth := range []int{64, 256, 1024, 2000} {
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			var nested interface{} = math.NaN()
			for i := 0; i < depth; i++ {
				nested = []interface{}{nested}
			}
			msg := Capture{Event: "nested", DistinctId: "user", Properties: Properties{"value": nested, "finite": 42.5, "literal": "NaN"}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, _, err := prepareForSend(msg, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type contextNumber = contextmarshaler.Number

type eventPropertyKey int

func (eventPropertyKey) MarshalText() ([]byte, error) { return []byte("text-key"), nil }

func TestPrepareCaptureNormalizationCompatibility(t *testing.T) {
	var pointer *float64
	got, data := preparedProperties(t, Capture{Event: "compatibility", DistinctId: "user", Properties: Properties{
		"number": math.NaN(), "context_number": contextNumber(math.NaN()),
		"context_pointer": []struct {
			Custom contextmarshaler.PointerProperty
		}{{contextmarshaler.PointerProperty(contextmarshaler.NewProperty("do-not-send", `{"redacted":true}`, nil))}},
		"bytes": []byte("ab"), "empty_bytes": []byte{}, "nil_bytes": []byte(nil),
		"nil_map": map[string]float64(nil), "nil_slice": []float64(nil), "nil_pointer": pointer,
		"text_key": map[eventPropertyKey]float64{1: math.Inf(1)},
		"struct": struct {
			Custom contextJSONProperty
			Finite float64
		}{contextmarshaler.NewProperty("do-not-send", `{"redacted":true}`, nil), 42},
	}})
	for key, want := range map[string]interface{}{
		"number": nil, "context_number": "context-number",
		"context_pointer": []interface{}{map[string]interface{}{"Custom": map[string]interface{}{"redacted": true}}}, "bytes": "YWI=", "empty_bytes": "", "nil_bytes": nil,
		"nil_map": nil, "nil_slice": nil, "nil_pointer": nil, "text_key": map[string]interface{}{"text-key": nil},
		"struct": map[string]interface{}{"Custom": map[string]interface{}{"redacted": true}, "Finite": float64(42)},
	} {
		require.Contains(t, got, key)
		require.Equal(t, want, got[key], key)
	}
	require.NotContains(t, string(data), "do-not-send")
	bad := Capture{Event: "bad nested context", DistinctId: "user", Properties: Properties{
		"number": math.NaN(),
		"value":  struct{ Custom contextJSONProperty }{contextmarshaler.NewProperty("do-not-send", `{"value":01}`, nil)},
	}}
	_, _, _, err := prepareForSend(bad, nil)
	require.Error(t, err, "nested custom output must not be replaced by ordinary struct fields")
}
