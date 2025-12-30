package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/posthog/posthog-go"
)

const etagPollInterval = 5 * time.Second

// loggingTransport wraps an http.RoundTripper to log ETag-related headers
type loggingTransport struct {
	wrapped      http.RoundTripper
	requestCount int64
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only log for local_evaluation endpoint
	if !strings.Contains(req.URL.Path, "local_evaluation") {
		return t.wrapped.RoundTrip(req)
	}

	reqNum := atomic.AddInt64(&t.requestCount, 1)
	timestamp := time.Now().Format(time.RFC3339)

	ifNoneMatch := req.Header.Get("If-None-Match")
	if ifNoneMatch == "" {
		ifNoneMatch = "(none)"
	}

	fmt.Printf("[%s] Request #%d\n", timestamp, reqNum)
	fmt.Printf("  If-None-Match: %s\n", ifNoneMatch)

	resp, err := t.wrapped.RoundTrip(req)
	if err != nil {
		fmt.Printf("  Error: %v\n\n", err)
		return resp, err
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag = "(none)"
	}

	statusText := ""
	if resp.StatusCode == http.StatusNotModified {
		statusText = " (Not Modified)"
	}

	fmt.Printf("  Status: %d%s\n", resp.StatusCode, statusText)
	fmt.Printf("  ETag: %s\n", etag)

	if resp.StatusCode == http.StatusNotModified {
		fmt.Println("  -> Using cached flags (no data transfer)")
	} else if resp.StatusCode == http.StatusOK {
		fmt.Println("  -> Received fresh flags")
	}
	fmt.Println()

	return resp, err
}

// TestETagPolling demonstrates ETag support for local evaluation polling.
// It polls every 5 seconds and logs the ETag behavior to show when
// 304 Not Modified responses occur (indicating no data transfer).
func TestETagPolling(projectAPIKey, personalAPIKey, endpointURL string) {
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("ETag Polling Test")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Host: %s\n", endpointURL)
	fmt.Printf("Poll interval: %v\n", etagPollInterval)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()

	// Create a logging transport that wraps the default transport
	transport := &loggingTransport{
		wrapped: http.DefaultTransport,
	}

	client, err := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		PersonalApiKey:                     personalAPIKey,
		Endpoint:                           endpointURL,
		DefaultFeatureFlagsPollingInterval: etagPollInterval,
		Transport:                          transport,
		Verbose:                            false,
	})
	if err != nil {
		fmt.Printf("Error creating client: %v\n", err)
		return
	}

	fmt.Println("Waiting for initial flag load...")
	fmt.Println()

	// Wait a moment for initial load
	time.Sleep(2 * time.Second)

	// Set up graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	// Periodically log flag state
	ticker := time.NewTicker(etagPollInterval + time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logETagFlagState(client)
		case <-sigChan:
			fmt.Println("\nShutting down...")
			client.Close()
			return
		}
	}
}

func logETagFlagState(client posthog.Client) {
	fmt.Println(strings.Repeat("-", 40))

	// Disable $feature_flag_called events to avoid /batch/ requests
	sendEvents := false

	// Test a flag to verify the client is working
	result, err := client.IsFeatureEnabled(posthog.FeatureFlagPayload{
		Key:                   "test-flag",
		DistinctId:            "test-user",
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if err != nil {
		fmt.Printf("Flag evaluation error: %v\n", err)
	} else {
		fmt.Printf("Sample flag 'test-flag': %v\n", result)
	}

	fmt.Println(strings.Repeat("-", 40))
	fmt.Println()
}
