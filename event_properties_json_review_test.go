package posthog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/posthog/posthog-go/testdata/contextmarshaler"
	"github.com/stretchr/testify/require"
)

type contextJSONProperty = contextmarshaler.Property

func TestCaptureContextMarshalerPrivacy(t *testing.T) {
	customErr := errors.New("context marshaler failed")
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, tc := range []struct {
			name, output string
			err          error
			wantFailure  bool
		}{
			{"valid", `{"redacted":true}`, nil, false},
			{"invalid JSON", `{"value":01}`, nil, true},
			{"explicit error", "", customErr, true},
		} {
			for _, withNonFinite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/nonfinite=%t", mode, tc.name, withNonFinite), func(t *testing.T) {
					input := contextmarshaler.NewProperty("do-not-send", tc.output, tc.err)
					props := Properties{"custom": input, "finite": 42.5, "literal": "NaN"}
					if withNonFinite {
						props["number"] = math.NaN()
					}
					var mu sync.Mutex
					var events []map[string]interface{}
					cb := &recordingCallback{}
					c, err := NewWithConfig("test-key", Config{
						Endpoint: "http://posthog.test", CaptureMode: mode, Callback: cb, Logger: quietTestLogger{t}, BatchSize: 100, Interval: time.Hour,
						Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
							body, err := io.ReadAll(r.Body)
							if err != nil {
								return nil, err
							}
							var envelope struct {
								Batch []map[string]interface{} `json:"batch"`
							}
							if err := json.Unmarshal(body, &envelope); err != nil {
								return nil, err
							}
							mu.Lock()
							events = append(events, envelope.Batch...)
							mu.Unlock()
							response := `{}`
							if mode == CaptureModeAnalyticsV1 {
								response = allOkResultsBody(body)
							}
							return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
						}),
					})
					require.NoError(t, err)
					t.Cleanup(func() { _ = c.Close() })
					require.NoError(t, c.Enqueue(Capture{Event: "custom", DistinctId: "user", Properties: props}))
					require.NoError(t, c.Enqueue(Capture{Event: "neighbor", DistinctId: "user", Properties: Properties{"finite": 7}}))
					require.NoError(t, c.Close())
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
					mu.Lock()
					defer mu.Unlock()
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
}

type countingJSONProperty struct{ calls int }

func (v *countingJSONProperty) MarshalJSON() ([]byte, error) {
	v.calls++
	return []byte(`"opaque"`), nil
}

func TestPrepareCaptureDeepPropertiesMarshalOpaqueValueOnce(t *testing.T) {
	const depth = 64
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			custom := &countingJSONProperty{}
			var nested interface{} = map[string]interface{}{"a_custom": custom, "z_number": math.NaN()}
			for i := 0; i < depth; i++ {
				nested = []interface{}{nested}
			}
			msg := Capture{Event: "nested", DistinctId: "user", Properties: Properties{"a_number": math.NaN(), "z_nested": nested}}
			var data []byte
			var err error
			if mode == CaptureModeLegacy {
				data, _, err = prepareForSend(msg)
			} else {
				data, _, _, err = prepareForSendV1(msg, nil)
			}
			require.NoError(t, err)
			require.Equal(t, 1, custom.calls, "normalization must not repeatedly invoke an opaque marshaler for overlapping subtrees")
			var wire struct {
				Properties map[string]interface{} `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(data, &wire))
			require.Nil(t, wire.Properties["a_number"])
			got := wire.Properties["z_nested"]
			for i := 0; i < depth; i++ {
				array, ok := got.([]interface{})
				require.True(t, ok)
				require.Len(t, array, 1)
				got = array[0]
			}
			require.Equal(t, map[string]interface{}{"a_custom": "opaque", "z_number": nil}, got)
		})
	}
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
				if _, _, err := prepareForSend(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPrepareCaptureFinite(b *testing.B) {
	msg := Capture{Event: "checkout", DistinctId: "user-123", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Properties: Properties{
		"amount": 42.5, "items": []interface{}{map[string]interface{}{"sku": "abc", "quantity": 2}, map[string]interface{}{"sku": "def", "quantity": 1}},
		"paid": true, "note": nil, "plan": "premium", "literal": "NaN",
	}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := prepareForSend(msg); err != nil {
			b.Fatal(err)
		}
	}
}

type contextNumber = contextmarshaler.Number

type eventPropertyKey int

func (eventPropertyKey) MarshalText() ([]byte, error) { return []byte("text-key"), nil }

func TestPrepareCaptureNormalizationCompatibility(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			var pointer *float64
			msg := Capture{Event: "compatibility", DistinctId: "user", Properties: Properties{
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
			}}
			var data []byte
			var err error
			if mode == CaptureModeLegacy {
				data, _, err = prepareForSend(msg)
			} else {
				data, _, _, err = prepareForSendV1(msg, nil)
			}
			require.NoError(t, err)
			var wire struct {
				Properties map[string]interface{} `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(data, &wire))
			for key, want := range map[string]interface{}{
				"number": nil, "context_number": "context-number",
				"context_pointer": []interface{}{map[string]interface{}{"Custom": map[string]interface{}{"redacted": true}}}, "bytes": "YWI=", "empty_bytes": "", "nil_bytes": nil,
				"nil_map": nil, "nil_slice": nil, "nil_pointer": nil, "text_key": map[string]interface{}{"text-key": nil},
				"struct": map[string]interface{}{"Custom": map[string]interface{}{"redacted": true}, "Finite": float64(42)},
			} {
				require.Contains(t, wire.Properties, key)
				require.Equal(t, want, wire.Properties[key], key)
			}
			require.NotContains(t, string(data), "do-not-send")
			bad := Capture{Event: "bad nested context", DistinctId: "user", Properties: Properties{"value": struct{ Custom contextJSONProperty }{contextmarshaler.NewProperty("do-not-send", `{"value":01}`, nil)}}}
			if mode == CaptureModeLegacy {
				_, _, err = prepareForSend(bad)
			} else {
				_, _, _, err = prepareForSendV1(bad, nil)
			}
			require.Error(t, err, "nested custom output must not be replaced by ordinary struct fields")
		})
	}
}
