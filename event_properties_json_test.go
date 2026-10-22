package posthog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gojson "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func TestCaptureNonFiniteProperties(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, number := range []struct {
			name  string
			value float64
		}{
			{"NaN", math.NaN()}, {"positive infinity", math.Inf(1)}, {"negative infinity", math.Inf(-1)},
		} {
			t.Run(fmt.Sprintf("%d/%s", mode, number.name), func(t *testing.T) {
				n := number.value
				nested := map[string]interface{}{"number": n, "finite": 42.5}
				array := []interface{}{n, nested, true, nil, "NaN", 42.5}
				typed := []float64{42.5, n, 7}
				props := Properties{
					"number": n, "float32": float32(n), "nested": nested, "array": array, "map_pointer": &nested,
					"typed_slice": typed, "typed_map": map[string]float64{"number": n, "finite": 42.5},
					"typed_array": [3]float32{1, float32(n), 3},
					"struct":      struct{ Number float64 }{n}, "pointer": &n,
					"finite": 42.5, "bool": true, "null": nil, "literal": "NaN", "infinity_literal": "Infinity",
					"custom": nonFiniteJSONControl(n), "text": nonFiniteTextControl(n),
					"custom_map":   nonFiniteMapControl{"number": n},
					"integer_keys": map[int]float64{1: n, 2: 42.5},
					"tagged": struct {
						Number float64 `json:"n"`
					}{42.5},
					"raw": gojson.RawMessage(`{"literal":"NaN"}`),
				}
				var mu sync.Mutex
				var batches [][]map[string]interface{}
				cb := &recordingCallback{}
				c, err := NewWithConfig("test-key", Config{
					Endpoint: "http://posthog.test", CaptureMode: mode, Callback: cb,
					Logger: quietTestLogger{t}, Interval: time.Hour, BatchSize: 100,
					Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							return nil, err
						}
						var envelope struct {
							Batch []map[string]interface{} `json:"batch"`
						}
						if err := json.Unmarshal(body, &envelope); err != nil {
							return nil, fmt.Errorf("request must be strict JSON: %w", err)
						}
						mu.Lock()
						batches = append(batches, envelope.Batch)
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
				require.NoError(t, c.Enqueue(Capture{Event: "nonfinite", DistinctId: "user", Properties: props}))
				require.NoError(t, c.Enqueue(Capture{Event: "neighbor", DistinctId: "user", Properties: Properties{"finite": 7}}))
				require.NoError(t, c.Close())
				successes, failures := cb.counts()
				for _, f := range cb.failures {
					t.Logf("failure: %T %v", f.err, f.err)
				}
				require.Equal(t, 0, failures, "non-finite properties must not reject an event: %v", cb.failures)
				require.Equal(t, 2, successes)
				mu.Lock()
				defer mu.Unlock()
				require.Len(t, batches, 1)
				require.Len(t, batches[0], 2)
				require.Equal(t, "nonfinite", batches[0][0]["event"])
				require.Equal(t, "neighbor", batches[0][1]["event"])
				require.Equal(t, float64(7), batches[0][1]["properties"].(map[string]interface{})["finite"])
				got := batches[0][0]["properties"].(map[string]interface{})
				for key, want := range map[string]interface{}{
					"number": nil, "float32": nil, "nested": map[string]interface{}{"number": nil, "finite": 42.5},
					"map_pointer": map[string]interface{}{"number": nil, "finite": 42.5},
					"array":       []interface{}{nil, map[string]interface{}{"number": nil, "finite": 42.5}, true, nil, "NaN", 42.5},
					"typed_slice": []interface{}{42.5, nil, float64(7)},
					"typed_map":   map[string]interface{}{"number": nil, "finite": 42.5},
					"typed_array": []interface{}{float64(1), nil, float64(3)}, "struct": nil, "pointer": nil,
					"finite": 42.5, "bool": true, "null": nil, "literal": "NaN", "infinity_literal": "Infinity", "custom": "custom-float", "text": "text-float",
					"custom_map":   map[string]interface{}{"custom": true},
					"integer_keys": map[string]interface{}{"1": nil, "2": 42.5},
					"tagged":       map[string]interface{}{"n": 42.5},
					"raw":          map[string]interface{}{"literal": "NaN"},
				} {
					require.Contains(t, got, key)
					require.Equal(t, want, got[key], key)
				}
				sameNumber := func(v float64) {
					if math.IsNaN(number.value) {
						require.True(t, math.IsNaN(v))
					} else {
						require.Equal(t, number.value, v)
					}
				}
				sameNumber(props["number"].(float64))
				sameNumber(nested["number"].(float64))
				sameNumber(array[0].(float64))
				sameNumber(typed[1])
				sameNumber(props["typed_map"].(map[string]float64)["number"])
				sameNumber(*props["pointer"].(*float64))
				sameNumber(cb.successes[0].(CaptureInApi).Properties["number"].(float64))
			})
		}
	}
}

type nonFiniteJSONControl float64

func (nonFiniteJSONControl) MarshalJSON() ([]byte, error) { return []byte(`"custom-float"`), nil }

func TestPrepareCaptureFloat32StrictJSON(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, n := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
			for _, tc := range []struct {
				name  string
				value interface{}
				want  interface{}
			}{
				{"scalar", n, nil},
				{"typed array", [3]float32{1, n, 3}, []interface{}{float64(1), nil, float64(3)}},
				{"struct", struct{ Number float32 }{n}, nil},
				{"nested map", map[string]interface{}{"value": n, "finite": float32(2)}, map[string]interface{}{"value": nil, "finite": float64(2)}},
				{"object in array", []interface{}{map[string]float32{"value": n}, "NaN"}, []interface{}{map[string]interface{}{"value": nil}, "NaN"}},
			} {
				t.Run(fmt.Sprintf("%d/%v/%s", mode, n, tc.name), func(t *testing.T) {
					msg := Capture{Event: "float32", DistinctId: "user", Properties: Properties{"value": tc.value}}
					var data []byte
					var err error
					if mode == CaptureModeLegacy {
						data, _, err = prepareForSend(msg)
					} else {
						data, _, _, err = prepareForSendV1(msg, nil)
					}
					require.NoError(t, err)
					require.True(t, json.Valid(data), "encoder returned nil error but invalid JSON: %s", data)
					var wire struct {
						Properties map[string]interface{} `json:"properties"`
					}
					require.NoError(t, json.Unmarshal(data, &wire))
					require.Contains(t, wire.Properties, "value")
					require.Equal(t, tc.want, wire.Properties["value"])
				})
			}
		}
	}
}

var errPropertyMarshaler = errors.New("property marshaler failed")

type failingPropertyMarshaler struct{ NonFiniteError bool }

func (v failingPropertyMarshaler) MarshalJSON() ([]byte, error) {
	if v.NonFiniteError {
		return nil, &gojson.UnsupportedValueError{Value: reflect.ValueOf(math.NaN()), Str: "NaN"}
	}
	return nil, errPropertyMarshaler
}

type nonFiniteTextControl float64

func (nonFiniteTextControl) MarshalText() ([]byte, error) { return []byte("text-float"), nil }

type nonFiniteMapControl map[string]float64

func (nonFiniteMapControl) MarshalJSON() ([]byte, error) { return []byte(`{"custom":true}`), nil }

func TestPrepareCaptureSerializationErrors(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, tc := range []struct {
			name  string
			value interface{}
			want  error
		}{
			{"unsupported channel", make(chan int), nil},
			{"unsupported function", func() {}, nil},
			{"custom error", failingPropertyMarshaler{}, errPropertyMarshaler},
			{"custom nonfinite error", failingPropertyMarshaler{true}, nil},
			{"invalid numeric literal", gojson.Number("NaN"), nil},
		} {
			for _, withNonFinite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/nonfinite=%t", mode, tc.name, withNonFinite), func(t *testing.T) {
					msg := Capture{Event: "unsupported", DistinctId: "user", Properties: Properties{"unsupported": tc.value}}
					if withNonFinite {
						msg.Properties["number"] = math.NaN()
					}
					var err error
					if mode == CaptureModeLegacy {
						_, _, err = prepareForSend(msg)
					} else {
						_, _, _, err = prepareForSendV1(msg, nil)
					}
					require.Error(t, err)
					require.False(t, isNonFiniteJSONError(err), "unrelated errors must not be classified as numeric failures")
					if tc.want != nil {
						require.ErrorIs(t, err, tc.want)
					}
					if strings.HasPrefix(tc.name, "custom") {
						var marshalerErr *gojson.MarshalerError
						require.ErrorAs(t, err, &marshalerErr)
					}
				})
			}
		}
	}
}

func TestPrepareCaptureCycles(t *testing.T) {
	if os.Getenv("POSTHOG_TEST_PROPERTY_CYCLES") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrepareCaptureCycles$")
		cmd.Env = append(os.Environ(), "POSTHOG_TEST_PROPERTY_CYCLES=1", "GOMEMLIMIT=128MiB")
		output, err := cmd.CombinedOutput()
		require.NoError(t, ctx.Err(), "cycle test timed out: %s", output)
		require.NoError(t, err, "%s", output)
		return
	}
	m := map[string]interface{}{"a_number": math.NaN()}
	m["z_cycle"] = m
	s := []interface{}{math.NaN(), nil}
	s[1] = s
	plain := map[string]interface{}{}
	plain["cycle"] = plain
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for name, value := range map[string]interface{}{"map": m, "map pointer": &m, "slice": s, "plain cycle": plain} {
			t.Run(fmt.Sprintf("%d/%s", mode, name), func(t *testing.T) {
				msg := Capture{Event: "cycle", DistinctId: "user", Properties: Properties{"value": value}}
				var err error
				if mode == CaptureModeLegacy {
					_, _, err = prepareForSend(msg)
				} else {
					_, _, _, err = prepareForSendV1(msg, nil)
				}
				require.Error(t, err)
				require.False(t, isNonFiniteJSONError(err))
			})
		}
	}
}

type redactedProperty struct{ Value string }

func (*redactedProperty) MarshalJSON() ([]byte, error) { return []byte(`"redacted"`), nil }

func TestPrepareCapturePreservesAddressableMarshalers(t *testing.T) {
	type row struct {
		Number     float64
		Credential redactedProperty
	}
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		rows := []row{{42, redactedProperty{"caller value"}}, {math.NaN(), redactedProperty{"caller value"}}}
		array := [2]row{rows[0], rows[1]}
		for name, value := range map[string]interface{}{"slice": rows, "slice pointer": &rows, "array pointer": &array} {
			t.Run(fmt.Sprintf("%d/%s", mode, name), func(t *testing.T) {
				msg := Capture{Event: "addressable", DistinctId: "user", Properties: Properties{"rows": value}}
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
				require.Equal(t, []interface{}{map[string]interface{}{"Number": float64(42), "Credential": "redacted"}, nil}, wire.Properties["rows"])
			})
		}
	}
}
