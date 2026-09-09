package posthog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

type nullPropertyMarshaler struct{}

func (nullPropertyMarshaler) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

type objectPropertyMarshaler struct{}

func (objectPropertyMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`{"drop":null,"large":9007199254740993}`), nil
}

type orderedPropertyMarshaler struct {
	data  string
	calls int
}

func (m *orderedPropertyMarshaler) MarshalJSON() ([]byte, error) {
	m.calls++
	return []byte(m.data), nil
}

// Compare raw property bytes: decoding the tested objects into maps would hide
// duplicate-member loss in the event serializer itself.
func TestEventPropertySerializationOrderedMembers(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"nonnull_duplicates", `{"x":1,"x":2,"drop":null}`, `{"x":1,"x":2}`},
		{"trailing_null", `{"x":1,"x":null}`, `{"x":1}`},
		{"leading_null", `{"x":null,"x":2}`, `{"x":2}`},
		{"all_null", `{"x":null,"x":null}`, `{}`},
		{"case_distinct", `{"Foo":1,"foo":2,"Foo":null,"FOO":3}`, `{"Foo":1,"foo":2,"FOO":3}`},
		{"escaped_keys", `{"x":1,"\u0078":null,"x":2,"quote\"":"null","\\":false}`, `{"x":1,"x":2,"quote\"":"null","\\":false}`},
		{"arrays_and_numbers", `{"items":[null,{"x":9007199254740993,"x":18446744073709551615,"x":null},[null,{"n":1.234567890123456789,"n":1e300,"n":-0,"n":1.2300e+04,"drop":null}],{},[],false,0,""]}`, `{"items":[null,{"x":9007199254740993,"x":18446744073709551615},[null,{"n":1.234567890123456789,"n":1e300,"n":-0,"n":1.2300e+04}],{},[],false,0,""]}`},
	}
	for _, v1 := range []bool{false, true} {
		for _, kind := range []string{"capture", "flag", "exception"} {
			for _, custom := range []bool{false, true} {
				for _, tc := range cases {
					t.Run(fmt.Sprintf("v1=%v/%s/custom=%v/%s", v1, kind, custom, tc.name), func(t *testing.T) {
						marshaler := &orderedPropertyMarshaler{data: tc.input}
						raw := json.RawMessage(tc.input)
						var value interface{} = raw
						if custom {
							value = marshaler
						}
						props := Properties{"ordered": value}
						plain, err := json.Marshal(props)
						if err != nil || string(plain) != `{"ordered":`+tc.input+`}` {
							t.Fatalf("generic Properties changed: %s, %v", plain, err)
						}
						marshaler.calls = 0
						var msg Message = Capture{Event: "ordinary", DistinctId: "test-user", Properties: props}
						if kind == "flag" {
							props["$feature_flag"] = "missing"
							props["$feature_flag_response"] = nil
							props["$feature/missing"] = nil
							msg = Capture{Event: "$feature_flag_called", DistinctId: "test-user", Properties: props}
						} else if kind == "exception" {
							msg = Exception{DistinctId: "test-user", Properties: props, ExceptionList: []ExceptionItem{{Type: "Test", Value: "test"}}}
						}
						var data json.RawMessage
						if v1 {
							data, _, _, err = prepareForSendV1(msg, nil)
						} else {
							data, _, err = prepareForSend(msg)
						}
						if err != nil {
							t.Fatal(err)
						}
						var envelope struct {
							Properties struct {
								Ordered  json.RawMessage `json:"ordered"`
								Response json.RawMessage `json:"$feature_flag_response"`
								Feature  json.RawMessage `json:"$feature/missing"`
							} `json:"properties"`
						}
						if err := json.Unmarshal(data, &envelope); err != nil {
							t.Fatal(err)
						}
						if got := string(envelope.Properties.Ordered); got != tc.want {
							t.Errorf("ordered bytes: got %s want %s", got, tc.want)
						}
						if kind == "flag" && (string(envelope.Properties.Response) != "null" || string(envelope.Properties.Feature) != "null") {
							t.Errorf("typed flag nulls changed: %s", data)
						}
						if custom && marshaler.calls != 1 {
							t.Errorf("MarshalJSON called %d times, want once", marshaler.calls)
						}
						if string(raw) != tc.input || marshaler.data != tc.input {
							t.Fatal("caller JSON mutated")
						}
					})
				}
			}
		}
	}
}

func TestEventPropertySerializationRejectedJSON(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			// The existing JSON marshaler also rejects out-of-range raw numbers.
			for _, input := range []string{`{"x":`, `{"x":1e400}`} {
				t.Run(fmt.Sprintf("v1=%v/custom=%v/%s", v1, custom, input), func(t *testing.T) {
					var value interface{} = json.RawMessage(input)
					if custom {
						value = &orderedPropertyMarshaler{data: input}
					}
					if _, err := json.Marshal(value); err == nil {
						t.Fatal("fixture must fail ordinary serialization")
					}
					msg := Capture{Event: "ordinary", DistinctId: "test-user", Properties: Properties{"invalid": value}}
					var err error
					if v1 {
						_, _, _, err = prepareForSendV1(msg, nil)
					} else {
						_, _, err = prepareForSend(msg)
					}
					if err == nil {
						t.Fatal("rejected JSON must still report a serialization error")
					}
				})
			}
		}
	}
}

type errorPropertyMarshaler struct{ err error }

func (m errorPropertyMarshaler) MarshalJSON() ([]byte, error) { return nil, m.err }

// Compare the original serializer's concrete error chain, not only errors.As:
// the private property adapter must not add a callback-visible wrapper, and
// user-provided MarshalJSON error layers must remain intact.
func TestEventPropertySerializationErrorCompatibility(t *testing.T) {
	sentinel := errors.New("custom property serialization failed")
	userWrapped := &json.MarshalerError{Type: reflect.TypeOf(""), Err: sentinel}
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, kind := range []string{"capture", "identify", "group", "exception"} {
			for _, tc := range []struct {
				name  string
				value interface{}
				cause error
			}{
				{"unsupported", func() {}, nil},
				{"custom", errorPropertyMarshaler{sentinel}, sentinel},
				{"custom_wrapped", errorPropertyMarshaler{userWrapped}, userWrapped},
			} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, kind, tc.name), func(t *testing.T) {
					props := Properties{"invalid": tc.value}
					var msg Message
					switch kind {
					case "capture":
						msg = Capture{Event: "test", DistinctId: "test-user", Properties: props}
					case "identify":
						msg = Identify{DistinctId: "test-user", Properties: props}
					case "group":
						msg = GroupIdentify{Type: "company", Key: "test", Properties: props}
					case "exception":
						msg = Exception{DistinctId: "test-user", Properties: props, ExceptionList: []ExceptionItem{{Type: "Test", Value: "test"}}}
					}
					var baseline, actual error
					if mode == CaptureModeAnalyticsV1 {
						_, baseline = json.Marshal(buildV1Event(msg.apifyEvent(), nil))
						_, _, _, actual = prepareForSendV1(msg, nil)
					} else {
						_, baseline = json.Marshal(msg.APIfy())
						_, _, actual = prepareForSend(msg)
					}
					if baseline == nil {
						t.Fatal("fixture must fail original serialization")
					}
					check := func(label string, got error) {
						t.Helper()
						for want := baseline; want != nil; want = errors.Unwrap(want) {
							if reflect.TypeOf(got) != reflect.TypeOf(want) || got.Error() != want.Error() {
								t.Errorf("%s error: got %T %v, want %T %v", label, got, got, want, want)
								return
							}
							if want == tc.cause && got != tc.cause {
								t.Errorf("%s replaced user error identity", label)
							}
							got = errors.Unwrap(got)
						}
						if got != nil {
							t.Errorf("%s added error layer: %v", label, got)
						}
					}
					check("prepare", actual)
					failures := make(chan error, 1)
					client, err := NewWithConfig("test-key", Config{
						CaptureMode: mode, Transport: testTransportOK,
						Callback: testCallback{nil, func(_ APIMessage, err error) { failures <- err }},
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := client.Enqueue(msg); err != nil {
						t.Error(err)
					}
					if err := client.Close(); err != nil {
						t.Error(err)
					}
					select {
					case err := <-failures:
						check("callback", err)
					default:
						t.Error("failure callback not triggered")
					}
				})
			}
		}
	}
}

func nullPropertyFixture() Properties {
	var ptr *string
	var m map[string]interface{}
	var s []interface{}
	return Properties{
		"test": nil, "pointer": ptr, "nilMap": m, "nilSlice": s,
		"rawNull": json.RawMessage(" null "), "customNull": nullPropertyMarshaler{},
		"nested": map[string]*string{"drop": nil},
		"struct": struct {
			Drop *string `json:"drop"`
			Keep int     `json:"keep"`
		}{Keep: 1},
		"raw":         json.RawMessage(`{"drop":null,"items":[null,{"drop":null}],"large":9007199254740993,"decimal":1.234567890123456789}`),
		"custom":      objectPropertyMarshaler{},
		"items":       []interface{}{"1", nil, 2, Properties{"drop": nil}, []interface{}{nil}},
		"emptyObject": Properties{}, "emptyArray": []interface{}{}, "empty": "", "zero": 0, "enabled": false,
		"literal": "null", "literalUndefined": "undefined", "large": uint64(18446744073709551615),
		"$set": Properties{"drop": nil}, "$group_set": Properties{"drop": nil},
	}
}

func assertNullProperties(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"test", "pointer", "nilMap", "nilSlice", "rawNull", "customNull", "missing", "hookNull"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s must be absent: %s", key, got[key])
		}
	}
	expected := map[string]string{
		"nested": `{}`, "struct": `{"keep":1}`, "raw": `{"items":[null,{}],"large":9007199254740993,"decimal":1.234567890123456789}`,
		"custom": `{"large":9007199254740993}`, "items": `["1",null,2,{},[null]]`, "emptyObject": `{}`, "emptyArray": `[]`,
		"empty": `""`, "zero": `0`, "enabled": `false`, "literal": `"null"`, "literalUndefined": `"undefined"`,
		"large": `18446744073709551615`, "$set": `{}`, "$group_set": `{}`,
	}
	for k, want := range expected {
		var actualValue, wantValue interface{}
		a := json.NewDecoder(bytes.NewReader(got[k]))
		a.UseNumber()
		b := json.NewDecoder(bytes.NewBufferString(want))
		b.UseNumber()
		if err := a.Decode(&actualValue); err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		_ = b.Decode(&wantValue)
		if !reflect.DeepEqual(actualValue, wantValue) {
			t.Errorf("%s: got %s want %s", k, got[k], want)
		}
	}
}

func TestEventPropertySerialization(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		for _, kind := range []string{"capture", "exception", "identify", "group"} {
			t.Run(fmt.Sprintf("v1=%v/%s", v1, kind), func(t *testing.T) {
				props := nullPropertyFixture()
				before, _ := json.Marshal(props)
				var msg Message
				switch kind {
				case "capture":
					msg = Capture{Event: "$ai_generation", DistinctId: "test-user", Properties: props}
				case "exception":
					msg = Exception{DistinctId: "test-user", Properties: props, ExceptionList: []ExceptionItem{{Type: "Test", Value: "test", Stacktrace: &ExceptionStacktrace{Type: "raw"}}}}
				case "identify":
					msg = Identify{DistinctId: "test-user", Properties: props}
				case "group":
					msg = GroupIdentify{Type: "company", Key: "test", Properties: props}
				}
				var data json.RawMessage
				var err error
				if v1 {
					data, _, _, err = prepareForSendV1(msg, nil)
				} else {
					data, _, err = prepareForSend(msg)
				}
				if err != nil {
					t.Fatal(err)
				}
				var envelope map[string]json.RawMessage
				_ = json.Unmarshal(data, &envelope)
				raw := envelope["properties"]
				if kind == "identify" && !v1 {
					raw = envelope["$set"]
				} else if kind == "identify" || kind == "group" {
					var p map[string]json.RawMessage
					_ = json.Unmarshal(raw, &p)
					if kind == "identify" {
						raw = p["$set"]
					} else {
						raw = p["$group_set"]
					}
				}
				assertNullProperties(t, raw)
				if kind == "exception" && !bytes.Contains(data, []byte(`"frames":null`)) {
					t.Errorf("typed exception metadata changed: %s", data)
				}
				after, _ := json.Marshal(props)
				if !bytes.Equal(before, after) {
					t.Fatal("caller properties mutated")
				}
			})
		}
	}
}

type loopbackPropertyTransport struct {
	url       string
	transport http.RoundTripper
}

func (g loopbackPropertyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != g.url {
		return nil, fmt.Errorf("non-test SDK request blocked: %s", r.URL)
	}
	return g.transport.RoundTrip(r)
}

func TestEventPropertySerializationWire(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, hook := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/hook=%v", mode, hook), func(t *testing.T) {
				bodies := make(chan []byte, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					bodies <- body
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":{}}`))
				}))
				defer server.Close()
				cfg := Config{Endpoint: server.URL, CaptureMode: mode, Interval: time.Hour, Transport: loopbackPropertyTransport{server.URL, server.Client().Transport}}
				if hook {
					cfg.BeforeSend = func(m Message) Message {
						if c, ok := m.(Capture); ok {
							if c.Event == "drop" {
								return nil
							}
							c.Properties["hookNull"] = nil
							c.Properties["hookItems"] = []interface{}{nil, Properties{"drop": nil}}
							return c
						}
						return m
					}
				}
				client, err := NewWithConfig("test-key", cfg)
				if err != nil {
					t.Fatal(err)
				}
				props := nullPropertyFixture()
				// Existing hook isolation turns common nil maps/slices into empty containers.
				if hook {
					delete(props, "nilMap")
					delete(props, "nilSlice")
					props["hookNilMap"] = map[string]interface{}(nil)
					props["hookNilSlice"] = []interface{}(nil)
				}
				before, _ := json.Marshal(props)
				for _, m := range []Message{Capture{Event: "test", DistinctId: "test-user", Properties: props}, Capture{Event: "only", DistinctId: "test-user", Properties: Properties{"test": nil}}, Exception{DistinctId: "test-user", Properties: props, ExceptionList: []ExceptionItem{{Type: "Test", Value: "test"}}}} {
					if err := client.Enqueue(m); err != nil {
						t.Fatal(err)
					}
				}
				if hook {
					_ = client.Enqueue(Capture{Event: "drop", DistinctId: "test-user"})
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				count := 0
				for len(bodies) > 0 {
					var batch struct {
						Batch []struct {
							Event      string          `json:"event"`
							Properties json.RawMessage `json:"properties"`
						} `json:"batch"`
					}
					if err := json.Unmarshal(<-bodies, &batch); err != nil {
						t.Fatal(err)
					}
					for _, e := range batch.Batch {
						count++
						if e.Event == "only" {
							var p map[string]json.RawMessage
							_ = json.Unmarshal(e.Properties, &p)
							if _, ok := p["test"]; ok {
								t.Error("null-only property retained")
							}
							continue
						}
						assertNullProperties(t, e.Properties)
						if hook {
							var p map[string]json.RawMessage
							_ = json.Unmarshal(e.Properties, &p)
							if string(p["hookNilMap"]) != "{}" || string(p["hookNilSlice"]) != "[]" {
								t.Errorf("existing hook clone container semantics changed: %s", e.Properties)
							}
						}
						if hook && e.Event == "test" {
							var p map[string]json.RawMessage
							_ = json.Unmarshal(e.Properties, &p)
							if string(p["hookItems"]) != `[null,{}]` {
								t.Errorf("hook items: %s", p["hookItems"])
							}
						}
					}
				}
				if count != 3 {
					t.Errorf("got %d events, want 3", count)
				}
				after, _ := json.Marshal(props)
				if !bytes.Equal(before, after) {
					t.Fatal("wire serialization or hook mutated caller properties")
				}
			})
		}
	}
}

func TestEventPropertySerializationScopeAndErrors(t *testing.T) {
	props := Properties{"test": nil, "nested": Properties{"drop": nil}}
	plain, err := json.Marshal(props)
	if err != nil || !bytes.Contains(plain, []byte(`"test":null`)) {
		t.Fatalf("public Properties JSON changed: %s %v", plain, err)
	}
	for _, v1 := range []bool{false, true} {
		msg := Capture{Event: "$feature_flag_called", DistinctId: "test-user", Properties: props, minimalFlagCalledEvent: true}
		var data json.RawMessage
		if v1 {
			data, _, _, err = prepareForSendV1(msg, nil)
		} else {
			data, _, err = prepareForSend(msg)
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"nested"`)) {
			t.Fatalf("minimal-event allowlist bypassed: %s", data)
		}
		invalid := Capture{Event: "test", DistinctId: "test-user", Properties: Properties{"unsupported": make(chan int)}}
		if v1 {
			_, _, _, err = prepareForSendV1(invalid, nil)
		} else {
			_, _, err = prepareForSend(invalid)
		}
		if err == nil {
			t.Fatal("unsupported values must still report serialization errors")
		}
	}
}
