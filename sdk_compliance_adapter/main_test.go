package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go/v2"
)

func TestPostFlagsWithRetryRetriesRetryableStatusThenSucceeds(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodPost)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"flag_keys_to_evaluate":["example"]}` {
			t.Fatalf("body = %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"featureFlags":{"example":true}}`))
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{"flag_keys_to_evaluate":["example"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestPostFlagsWithRetryStopsAfterRetryableFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{}`))
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error")
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestPostFlagsWithRetryDoesNotRetryNonRetryableStatus(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{}`))
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error")
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestBuildCaptureKeepsAValidUUIDAndReplacesOthers(t *testing.T) {
	supplied := "0190d4a3-8f2b-7c3e-9a1d-5b6c7d8e9f00"
	if got := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", UUID: supplied}).Uuid; got != supplied {
		t.Fatalf("uuid = %q, want the supplied %q", got, supplied)
	}

	// The SDK replaces an invalid UUID, so the adapter must too, or its
	// response would name a UUID that was never sent.
	for _, supplied := range []string{"", "not-a-uuid"} {
		generated := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", UUID: supplied}).Uuid
		parsed, err := uuid.Parse(generated)
		if err != nil {
			t.Fatalf("uuid for %q = %q, which does not parse: %v", supplied, generated, err)
		}
		if parsed.Version() != 7 {
			t.Fatalf("uuid for %q has version %d, want 7", supplied, parsed.Version())
		}
	}
}

func TestBuildCapturePassesOptionsUnchanged(t *testing.T) {
	options := posthog.Options{"process_person_profile": "no", "future_option": map[string]interface{}{"nested": true}}
	got := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", Options: options}).Options
	if !reflect.DeepEqual(got, options) {
		t.Fatalf("options = %v, want %v", got, options)
	}
}

func TestCompressionFromEnv(t *testing.T) {
	cases := []struct {
		env    string
		want   string
		wantOk bool
	}{
		{"", "gzip", true},
		{"gzip", "gzip", true},
		{"deflate", "deflate", true},
		{"br", "br", true},
		{"zstd", "zstd", true},
		{"brotli", "brotli", false},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("COMPRESSION", tc.env)
			got, ok := compressionFromEnv()
			if got != tc.want || ok != tc.wantOk {
				t.Fatalf("compressionFromEnv() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
}
