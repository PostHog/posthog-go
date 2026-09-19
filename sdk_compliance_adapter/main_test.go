package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func action(t *testing.T, handler http.HandlerFunc, body string) map[string]interface{} {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("action status %d: %s", recorder.Code, recorder.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func mockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	port := os.Getenv("COMPLIANCE_TEST_PORT")
	if port == "" {
		port = "0"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(closeAndReset)
	return server
}

func profile(t *testing.T, mode, codec string) {
	t.Helper()
	oldMode, oldCodec := captureMode, compression
	captureMode, compression = mode, codec
	t.Cleanup(func() { captureMode, compression = oldMode, oldCodec })
}

func TestFeatureFlagHandlerUsesSDKEvaluation(t *testing.T) {
	for _, status := range []int{502, 504, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			profile(t, "v0", "gzip")
			var mu sync.Mutex
			var flags []map[string]interface{}
			var events []map[string]interface{}
			server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/flags/" {
					if r.URL.RawQuery != "v=2" || r.Header.Get("Authorization") != "" {
						t.Errorf("unexpected flags request: %v", r)
					}
					flags = append(flags, body)
					if len(flags) == 1 {
						w.WriteHeader(status)
						return
					}
					io.WriteString(w, `{"featureFlags":{"example":"variant-a"}}`)
					return
				}
				for _, event := range body["batch"].([]interface{}) {
					events = append(events, event.(map[string]interface{}))
				}
				io.WriteString(w, `{}`)
			})
			action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`"}`)
			payload := `{"key":"example","distinct_id":"user","person_properties":{"$device_id":"device"},"groups":{"company":"acme"},"group_properties":{"company":{"plan":"enterprise"}},"disable_geoip":false}`
			if status == 400 {
				recorder := httptest.NewRecorder()
				featureFlagHandler(recorder, httptest.NewRequest("POST", "/", bytes.NewBufferString(payload)))
				if recorder.Code != 500 {
					t.Fatalf("status = %d", recorder.Code)
				}
			} else {
				result := action(t, featureFlagHandler, payload)
				if result["value"] != "variant-a" {
					t.Fatalf("SDK value = %v", result)
				}
				// A fresh snapshot makes another remote request, but SDK exposure dedup remains intact.
				action(t, featureFlagHandler, payload)
			}
			action(t, flushHandler, `{}`)
			mu.Lock()
			defer mu.Unlock()
			expected := 3
			if status == 400 {
				expected = 1
			}
			if len(flags) != expected {
				t.Fatalf("flags requests = %d, want %d", len(flags), expected)
			}
			first := flags[0]
			if !reflect.DeepEqual(first["flag_keys_to_evaluate"], []interface{}{"example"}) || first["api_key"] != "test-key" || first["distinct_id"] != "user" {
				t.Fatalf("SDK payload = %v", first)
			}
			if first["person_properties"].(map[string]interface{})["$device_id"] != "device" || first["groups"].(map[string]interface{})["company"] != "acme" || first["group_properties"].(map[string]interface{})["company"].(map[string]interface{})["plan"] != "enterprise" {
				t.Fatalf("SDK properties = %v", first)
			}
			if _, exists := first["geoip_disable"]; exists {
				t.Fatalf("SDK serializes false GeoIP by omission: %v", first)
			}
			if status != 400 {
				if len(events) != 1 || events[0]["event"] != "$feature_flag_called" {
					t.Fatalf("SDK exposure events = %v", events)
				}
				props := events[0]["properties"].(map[string]interface{})
				if props["$feature_flag"] != "example" || props["$feature_flag_response"] != "variant-a" {
					t.Fatalf("exposure properties = %v", props)
				}
			} else if len(events) != 0 {
				t.Fatalf("unexpected events = %v", events)
			}
		})
	}
}

func TestFeatureFlagHandlerCompletesExposureBeforeReset(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags/" {
			io.WriteString(w, `{"featureFlags":{"example":true}}`)
			return
		}
		io.WriteString(w, `{}`)
	})
	// Keep the SDK exposure queued long enough to detect an early return from
	// the flag action; delivery still runs on the SDK's configured interval.
	action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`","flush_at":100,"flush_interval_ms":100}`)
	result := action(t, featureFlagHandler, `{"key":"example","distinct_id":"user"}`)
	if result["value"] != true {
		t.Fatalf("flag value = %v", result)
	}
	state.mu.Lock()
	pending, sent := state.pendingEvents, state.totalEventsSent
	state.mu.Unlock()
	if pending != 0 || sent != 1 {
		t.Fatalf("flag action returned before exposure completion: pending=%d sent=%d", pending, sent)
	}
	action(t, resetHandler, `{}`)
}

func TestFeatureFlagHandlerPreservesDefaultGeoIP(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/flags/" && body["geoip_disable"] != true {
			t.Errorf("SDK GeoIP default = %v", body["geoip_disable"])
		}
		io.WriteString(w, `{"featureFlags":{}}`)
	})
	action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`"}`)
	result := action(t, featureFlagHandler, `{"key":"unknown","distinct_id":"user"}`)
	if result["value"] != nil {
		t.Fatalf("unknown snapshot flag = %v", result)
	}
}

func TestCaptureFlushTracksSDKCompletionAndCodecs(t *testing.T) {
	for _, mode := range []string{"v0", "v1"} {
		codecs := []string{"gzip"}
		if mode == "v1" {
			codecs = append(codecs, "deflate", "br", "zstd")
		}
		for _, codec := range codecs {
			t.Run(mode+"/"+codec, func(t *testing.T) {
				profile(t, mode, codec)
				var mu sync.Mutex
				var ids []string
				server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
					if got := r.Header.Get("Content-Encoding"); got != codec {
						t.Errorf("encoding = %q, want %q", got, codec)
					}
					raw, _ := io.ReadAll(r.Body)
					decoded, err := decodeBody(raw, codec)
					if err != nil {
						t.Error(err)
						return
					}
					var body struct {
						Batch []struct {
							UUID string `json:"uuid"`
						} `json:"batch"`
					}
					if err := json.Unmarshal(decoded, &body); err != nil {
						t.Error(err)
						return
					}
					id := body.Batch[0].UUID
					mu.Lock()
					ids = append(ids, id)
					attempt := len(ids)
					mu.Unlock()
					if attempt == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(503)
						return
					}
					if mode == "v1" {
						io.WriteString(w, `{"results":{"`+id+`":{"result":"ok"}}}`)
					} else {
						io.WriteString(w, `{}`)
					}
				})
				action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`","enable_compression":true}`)
				result := action(t, captureHandler, `{"event":"test","distinct_id":"user"}`)
				if err := uuid.Validate(result["uuid"].(string)); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				action(t, flushHandler, `{}`)
				if time.Since(start) < 900*time.Millisecond {
					t.Fatal("flush returned before SDK retry completion")
				}
				mu.Lock()
				if !reflect.DeepEqual(ids, []string{result["uuid"].(string), result["uuid"].(string)}) {
					t.Errorf("wire UUIDs = %v, response = %v", ids, result)
				}
				mu.Unlock()
				state.mu.Lock()
				defer state.mu.Unlock()
				if state.pendingEvents != 0 || state.totalEventsSent != 1 || state.totalEventsCaptured != 1 || state.totalRetries != 1 {
					t.Fatalf("pending=%d sent=%d captured=%d retries=%d", state.pendingEvents, state.totalEventsSent, state.totalEventsCaptured, state.totalRetries)
				}
				for i, request := range state.requestsMade {
					if request.EventCount != 1 || request.RetryAttempt != i || !reflect.DeepEqual(request.UUIDList, []string{result["uuid"].(string)}) {
						t.Errorf("tracked request = %+v", request)
					}
				}
			})
		}
	}
}

func TestResetClosesPendingClientBeforeClearingState(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	for _, handler := range []http.HandlerFunc{resetHandler, initHandler} {
		action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`","flush_at":100,"flush_interval_ms":10000}`)
		action(t, captureHandler, `{"event":"test","distinct_id":"user"}`)
		action(t, handler, `{"api_key":"test-key","host":"`+server.URL+`"}`)
		state.mu.Lock()
		if state.pendingEvents != 0 || state.totalEventsSent != 0 || state.totalEventsCaptured != 0 || len(state.requestsMade) != 0 {
			t.Error("old-client delivery contaminated reset state")
		}
		state.mu.Unlock()
	}
}

func TestFlushCompletesOnTerminalFailure(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`"}`)
	action(t, captureHandler, `{"event":"test","distinct_id":"user"}`)
	action(t, flushHandler, `{}`)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingEvents != 0 || state.totalEventsSent != 0 || state.lastError == "" {
		t.Fatalf("terminal state: pending=%d sent=%d error=%q", state.pendingEvents, state.totalEventsSent, state.lastError)
	}
}

func TestV1PartialCompletionCountsOnlySuccessfulEvents(t *testing.T) {
	profile(t, "v1", "gzip")
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []struct {
				UUID string `json:"uuid"`
			} `json:"batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Batch) != 2 {
			t.Errorf("batch size = %d", len(body.Batch))
			return
		}
		io.WriteString(w, `{"results":{"`+body.Batch[0].UUID+`":{"result":"ok"},"`+body.Batch[1].UUID+`":{"result":"drop"}}}`)
	})
	action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`","flush_at":2,"flush_interval_ms":10000}`)
	action(t, captureHandler, `{"event":"first","distinct_id":"user"}`)
	action(t, captureHandler, `{"event":"second","distinct_id":"user"}`)
	action(t, flushHandler, `{}`)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingEvents != 0 || state.totalEventsSent != 1 || state.totalEventsCaptured != 2 || state.lastError == "" {
		t.Fatalf("partial completion: pending=%d sent=%d captured=%d error=%q", state.pendingEvents, state.totalEventsSent, state.totalEventsCaptured, state.lastError)
	}
}

func TestFlushDoesNotClaimCompletionWhenCanceled(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	action(t, initHandler, `{"api_key":"test-key","host":"`+server.URL+`","flush_at":100,"flush_interval_ms":10000}`)
	action(t, captureHandler, `{"event":"test","distinct_id":"user"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	flushHandler(recorder, httptest.NewRequest("POST", "/flush", nil).WithContext(ctx))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("flush status = %d", recorder.Code)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingEvents != 1 || state.totalEventsSent != 0 {
		t.Fatal("flush changed pending SDK delivery")
	}
}

func TestValidateHarnessHost(t *testing.T) {
	for _, host := range []string{"http://127.0.0.1:19230", "http://localhost:8081/", "http://test-harness:8081", "http://[::1]:19231"} {
		if _, ok := validateHarnessHost(host); !ok {
			t.Errorf("rejected %s", host)
		}
	}
	for _, host := range []string{"https://127.0.0.1:19230", "http://posthog.com:8081", "http://localhost:0", "http://localhost:65536", "http://localhost", "http://user@localhost:8081", "http://localhost:8081/path", "http://localhost:8081?x=1", "http://localhost:8081#fragment"} {
		if _, ok := validateHarnessHost(host); ok {
			t.Errorf("accepted %s", host)
		}
	}
}

func TestCompressionProfiles(t *testing.T) {
	for _, mode := range []string{"v0", "v1"} {
		for _, codec := range []string{"gzip", "deflate", "br", "zstd"} {
			t.Run(mode+"/"+codec, func(t *testing.T) {
				profile(t, mode, codec)
				_, err := selectedCompression()
				if mode == "v0" && codec != "gzip" {
					if err == nil {
						t.Fatal("accepted V1 codec for V0")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				health := action(t, healthHandler, `{}`)
				if !reflect.DeepEqual(health["capabilities"], []interface{}{"capture_" + mode, "encoding_" + codec}) {
					t.Fatalf("capabilities = %v", health)
				}
			})
		}
	}
}
