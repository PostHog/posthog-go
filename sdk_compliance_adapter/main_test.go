package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
