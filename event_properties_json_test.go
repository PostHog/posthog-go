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

// captureWire enqueues msgs through a client whose transport accepts every
// event, and returns the events and bodies of the requests it sent.
func captureWire(t *testing.T, msgs ...Message) ([]map[string]interface{}, []string, *recordingCallback) {
	t.Helper()
	var mu sync.Mutex
	var events []map[string]interface{}
	var bodies []string
	cb := &recordingCallback{}
	c, err := NewWithConfig("test-key", Config{
		Endpoint: "http://posthog.test", Callback: cb, Logger: quietTestLogger{t}, Interval: time.Hour, BatchSize: 100,
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
			events = append(events, envelope.Batch...)
			bodies = append(bodies, string(body))
			mu.Unlock()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(allOkResultsBody(body)))}, nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	for _, msg := range msgs {
		require.NoError(t, c.Enqueue(msg))
	}
	require.NoError(t, c.Close())
	mu.Lock()
	defer mu.Unlock()
	return events, bodies, cb
}

// preparedProperties runs msg through prepareForSend and returns its properties
// decoded from strict JSON.
func preparedProperties(t *testing.T, msg Message) (map[string]interface{}, []byte) {
	t.Helper()
	data, _, _, err := prepareForSend(msg, nil)
	require.NoError(t, err)
	require.True(t, json.Valid(data), "encoder returned nil error but invalid JSON: %s", data)
	var wire struct {
		Properties map[string]interface{} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	return wire.Properties, data
}

func TestCaptureNonFiniteProperties(t *testing.T) {
	for _, number := range []struct {
		name  string
		value float64
	}{
		{"NaN", math.NaN()}, {"positive infinity", math.Inf(1)}, {"negative infinity", math.Inf(-1)},
	} {
		t.Run(number.name, func(t *testing.T) {
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
			events, bodies, cb := captureWire(t,
				Capture{Event: "nonfinite", DistinctId: "user", Properties: props},
				Capture{Event: "neighbor", DistinctId: "user", Properties: Properties{"finite": 7}})
			successes, failures := cb.counts()
			require.Equal(t, 0, failures, "non-finite properties must not reject an event: %v", cb.failures)
			require.Equal(t, 2, successes)
			require.Len(t, bodies, 1)
			require.Len(t, events, 2)
			require.Equal(t, "nonfinite", events[0]["event"])
			require.Equal(t, "neighbor", events[1]["event"])
			require.Equal(t, float64(7), events[1]["properties"].(map[string]interface{})["finite"])
			got := events[0]["properties"].(map[string]interface{})
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

type nonFiniteJSONControl float64

func (nonFiniteJSONControl) MarshalJSON() ([]byte, error) { return []byte(`"custom-float"`), nil }

func TestPrepareCaptureFloat32StrictJSON(t *testing.T) {
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
			t.Run(fmt.Sprintf("%v/%s", n, tc.name), func(t *testing.T) {
				// go-json reports only a float64, so the float64 sends the event down the path that also nulls float32 values.
				got, _ := preparedProperties(t, Capture{Event: "float32", DistinctId: "user", Properties: Properties{"value": tc.value, "number": math.NaN()}})
				require.Contains(t, got, "value")
				require.Equal(t, tc.want, got["value"])
			})
		}
	}
}

func TestPrepareCaptureNormalizesOnlyNonFiniteEvents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		number    interface{}
		wire      interface{}
		wantCalls int
	}{
		{"finite", 42.5, 42.5, 1},
		// Normalization marshals the custom value again, so a second call shows the slower path ran.
		{"non-finite", math.NaN(), nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			custom := &countingJSONProperty{}
			event := func(number interface{}) Capture {
				return Capture{Uuid: "u", Event: "e", DistinctId: "user", Timestamp: time.Unix(0, 0), Properties: Properties{
					// A slice keeps the custom value ahead of the number; go-json visits map keys in random order.
					"list": []interface{}{custom, number}, "float32": float32(1.5), "bytes": []byte("ab"), "literal": "NaN",
					"nested": map[string]interface{}{"list": []interface{}{1, "two", nil}, "flag": true},
					"struct": struct{ Number float64 }{7}, "keys": map[eventPropertyKey]float64{1: 2},
				}}
			}
			data, _, _, err := prepareForSend(event(tc.number), nil)
			require.NoError(t, err)
			require.Equal(t, tc.wantCalls, custom.calls)
			want, err := gojson.Marshal(buildEvent(event(tc.wire).apifyEvent(), nil))
			require.NoError(t, err)
			require.Equal(t, string(want), string(data), "only the non-finite number may change on the wire")
		})
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
			t.Run(fmt.Sprintf("%s/nonfinite=%t", tc.name, withNonFinite), func(t *testing.T) {
				msg := Capture{Event: "unsupported", DistinctId: "user", Properties: Properties{"unsupported": tc.value}}
				if withNonFinite {
					msg.Properties["number"] = math.NaN()
				}
				data, apiMsg, _, err := prepareForSend(msg, nil)
				require.Error(t, err)
				require.Nil(t, data)
				require.NotNil(t, apiMsg)
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

func TestCaptureSerializationErrorDropsOnlyThatEvent(t *testing.T) {
	events, _, cb := captureWire(t,
		Capture{Event: "broken", DistinctId: "user", Properties: Properties{"number": math.NaN(), "custom": failingPropertyMarshaler{}}},
		Capture{Event: "neighbor", DistinctId: "user", Properties: Properties{"number": math.NaN()}})
	successes, failures := cb.counts()
	require.Equal(t, 1, successes)
	require.Equal(t, 1, failures)
	var local *CaptureLocalError
	require.ErrorAs(t, cb.failures[0].err, &local)
	require.ErrorIs(t, cb.failures[0].err, errPropertyMarshaler)
	require.Equal(t, "broken", cb.failures[0].msg.(CaptureInApi).Event)
	require.Len(t, events, 1)
	require.Equal(t, "neighbor", events[0]["event"])
	neighbor := events[0]["properties"].(map[string]interface{})
	require.Contains(t, neighbor, "number")
	require.Nil(t, neighbor["number"])
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
	for name, value := range map[string]interface{}{"map": m, "map pointer": &m, "slice": s, "plain cycle": plain} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := prepareForSend(Capture{Event: "cycle", DistinctId: "user", Properties: Properties{"value": value}}, nil)
			require.Error(t, err)
			require.False(t, isNonFiniteJSONError(err))
		})
	}
}

type redactedProperty struct{ Value string }

func (*redactedProperty) MarshalJSON() ([]byte, error) { return []byte(`"redacted"`), nil }

func TestPrepareCapturePreservesAddressableMarshalers(t *testing.T) {
	type row struct {
		Number     float64
		Credential redactedProperty
	}
	rows := []row{{42, redactedProperty{"caller value"}}, {math.NaN(), redactedProperty{"caller value"}}}
	array := [2]row{rows[0], rows[1]}
	for name, value := range map[string]interface{}{"slice": rows, "slice pointer": &rows, "array pointer": &array} {
		t.Run(name, func(t *testing.T) {
			got, _ := preparedProperties(t, Capture{Event: "addressable", DistinctId: "user", Properties: Properties{"rows": value}})
			require.Equal(t, []interface{}{map[string]interface{}{"Number": float64(42), "Credential": "redacted"}, nil}, got["rows"])
		})
	}
}

// typicalBenchmarkEvent is a web-style capture of about 1 KB.
func typicalBenchmarkEvent() Capture {
	return Capture{Event: "$pageview", DistinctId: "user-123", Uuid: "0190b6a2-7c3e-7f00-8000-000000000001",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Properties: Properties{
			"$current_url": "https://example.com/pricing?plan=team&utm_source=newsletter", "$host": "example.com",
			"$pathname": "/pricing", "$browser": "Chrome", "$browser_version": 129.0, "$os": "Mac OS X",
			"$os_version": "14.6.1", "$device_type": "Desktop", "$screen_height": 1117, "$screen_width": 1728,
			"$viewport_height": 965, "$viewport_width": 1728, "$referrer": "https://news.example.org/",
			"$referring_domain": "news.example.org", "$session_id": "0190b6a2-7c3e-7f00-8000-0000000000aa",
			"plan": "team", "seats": 12, "amount": 42.5, "paid": true, "note": nil, "literal": "NaN",
			"items":   []interface{}{map[string]interface{}{"sku": "abc", "quantity": 2}, map[string]interface{}{"sku": "def", "quantity": 1}},
			"$set":    map[string]interface{}{"email": "user@example.com", "Name": "Example User", "company": "Example Inc"},
			"$groups": map[string]interface{}{"company": "example-inc"},
		}}
}

// largeAIBenchmarkEvent is an $ai_generation of about 450 KB, mostly message text.
func largeAIBenchmarkEvent() Capture {
	paragraph := "The user asked how to configure retries for a flaky service. " +
		"I suggested backoff with jitter and returned {\"key\": [1, 2]}, tabs\tand newlines\n. "
	content := strings.Repeat(paragraph, 255)
	input := make([]interface{}, 0, 12)
	for i := 0; i < 6; i++ {
		input = append(input, map[string]interface{}{"role": "user", "content": content})
		input = append(input, map[string]interface{}{"role": "assistant", "content": content})
	}
	return Capture{Event: "$ai_generation", DistinctId: "user-123", Uuid: "0190b6a2-7c3e-7f00-8000-000000000002",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Properties: Properties{
			"$ai_trace_id": "trace-1", "$ai_model": "example-model", "$ai_provider": "example",
			"$ai_input": input, "$ai_input_tokens": 120000, "$ai_output_tokens": 900, "$ai_latency": 3.25,
			"$ai_output_choices": []interface{}{map[string]interface{}{"role": "assistant", "content": content[:4096]}},
			"$ai_total_cost_usd": 0.0123, "$ai_http_status": 200, "$ai_is_error": false,
		}}
}

var benchmarkPreparedSink json.RawMessage

func BenchmarkPrepareForSendFinite(b *testing.B) {
	for _, bench := range []struct {
		name string
		msg  Capture
	}{{"typical", typicalBenchmarkEvent()}, {"ai_large", largeAIBenchmarkEvent()}} {
		msg := bench.msg
		b.Run(bench.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				data, _, _, err := prepareForSend(msg, nil)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkPreparedSink = data
			}
		})
	}
}
