package posthog

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// Exercise both flag producers through their real evaluation and capture HTTP paths.
func TestFlagPropertySerializationWire(t *testing.T) {
	for _, mode := range []CaptureMode{CaptureModeLegacy, CaptureModeAnalyticsV1} {
		for _, snapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/snapshot=%v", mode, snapshot), func(t *testing.T) {
				bodies := make(chan []byte, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.HasPrefix(r.URL.Path, "/flags") {
						_, _ = w.Write([]byte(`{"flags":{"plain":{"key":"plain","enabled":false,"metadata":{"id":1,"version":1,"has_experiment":false}}},"minimalFlagCalledEvents":true,"errorsWhileComputingFlags":true}`))
						return
					}
					body, _ := io.ReadAll(r.Body)
					bodies <- body
					_, _ = w.Write([]byte(`{"results":{}}`))
				}))
				defer server.Close()
				client, err := NewWithConfig("test-key", Config{
					Endpoint: server.URL, CaptureMode: mode, Interval: time.Hour,
					Transport: loopbackPropertyTransport{server.URL, server.Client().Transport},
					BeforeSend: func(m Message) Message {
						if c, ok := m.(Capture); ok {
							c.Properties["customNull"] = nil
							c.Properties["customKeep"] = true
							c.Properties["$feature/other"] = nil
							c.Properties["$feature_flag_response_extra"] = nil
							c.Properties["items"] = []interface{}{nil, Properties{"drop": nil}}
							return c
						}
						return m
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				if snapshot {
					flags, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "test-user"})
					if err != nil {
						t.Fatal(err)
					}
					if flags.IsEnabled("missing") || flags.IsEnabled("plain") {
						t.Fatal("unexpected enabled flag")
					}
				} else {
					result, err := client.GetFeatureFlagResult(FeatureFlagPayload{Key: "missing", DistinctId: "test-user"})
					if result != nil || err == nil {
						t.Fatalf("missing result: %v %v", result, err)
					}
					result, err = client.GetFeatureFlagResult(FeatureFlagPayload{Key: "plain", DistinctId: "test-user"})
					if err != nil || result == nil || result.Enabled {
						t.Fatalf("plain result: %v %v", result, err)
					}
				}
				if err := client.Enqueue(Capture{Event: "ordinary", DistinctId: "test-user", Properties: Properties{
					"$feature_flag": "missing", "$feature_flag_response": nil, "$feature/missing": nil,
				}}); err != nil {
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				count := 0
				for len(bodies) > 0 {
					var batch struct {
						Batch []struct {
							Event      string                     `json:"event"`
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"batch"`
					}
					if err := json.Unmarshal(<-bodies, &batch); err != nil {
						t.Fatal(err)
					}
					for _, e := range batch.Batch {
						count++
						p := e.Properties
						for _, key := range []string{"customNull", "$feature/other", "$feature_flag_response_extra"} {
							if _, ok := p[key]; ok {
								t.Errorf("custom key %s retained: %s", key, p[key])
							}
						}
						if e.Event == "ordinary" {
							for _, key := range []string{"$feature_flag_response", "$feature/missing"} {
								if _, ok := p[key]; ok {
									t.Errorf("ordinary event retained %s", key)
								}
							}
						} else if string(p["$feature_flag"]) == `"missing"` {
							if string(p["$feature_flag_response"]) != "null" {
								t.Errorf("generated null response missing: %v", p)
							}
							if snapshot && string(p["$feature/missing"]) != "null" {
								t.Errorf("generated exact feature null missing: %v", p)
							}
							if !snapshot {
								if _, ok := p["$feature/missing"]; ok {
									t.Error("invented feature field")
								}
							}
							if !strings.Contains(string(p["$feature_flag_error"]), "flag_missing") {
								t.Error("missing flag error lost")
							}
							if !strings.Contains(string(p["$feature_flag_error"]), "errors_while_computing_flags") {
								t.Error("evaluation error lost")
							}
							if string(p["items"]) != `[null,{}]` || string(p["customKeep"]) != "true" {
								t.Errorf("custom siblings changed: %v", p)
							}
						} else {
							if string(p["$feature_flag_response"]) != "false" {
								t.Errorf("false response changed: %v", p)
							}
							for _, key := range []string{"$feature/plain", "items", "customKeep"} {
								if _, ok := p[key]; ok {
									t.Errorf("minimal privacy boundary bypassed: %s", key)
								}
							}
						}
					}
				}
				if count != 3 {
					t.Fatalf("got %d events, want 3", count)
				}
			})
		}
	}
}

func TestFlagPropertySerializationScope(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		for _, minimal := range []bool{false, true} {
			msg := Capture{Event: "$feature_flag_called", DistinctId: "test-user", minimalFlagCalledEvent: minimal, Properties: Properties{
				"$feature_flag": "missing", "$feature_flag_response": nil, "$feature/missing": nil,
				"$feature/other": nil, "custom": nil,
			}}
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
			var e struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			if string(e.Properties["$feature_flag_response"]) != "null" {
				t.Errorf("v1=%v minimal=%v response missing: %s", v1, minimal, data)
			}
			_, hasFeature := e.Properties["$feature/missing"]
			if hasFeature == minimal {
				t.Errorf("privacy boundary changed: %s", data)
			}
			for _, key := range []string{"$feature/other", "custom"} {
				if _, ok := e.Properties[key]; ok {
					t.Errorf("custom key retained: %s", data)
				}
			}
		}
	}
	// Non-null custom objects in typed-looking fields still normalize recursively.
	data, err := json.Marshal(flagEventProperties("$feature_flag_called", Properties{
		"$feature_flag": "key", "$feature_flag_response": Properties{"drop": nil},
		"$feature/key": Properties{"items": []interface{}{nil, Properties{"drop": nil}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if string(p["$feature_flag_response"]) != "{}" || string(p["$feature/key"]) != `{"items":[null,{}]}` {
		t.Fatalf("broad typed-field exemption: %s", data)
	}
}
