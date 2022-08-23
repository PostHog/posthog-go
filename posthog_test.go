package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Helper type used to implement the io.Reader interface on function values.
type readFunc func([]byte) (int, error)

func (f readFunc) Read(b []byte) (int, error) { return f(b) }

// Helper type used to implement the http.RoundTripper interface on function
// values.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (f roundTripperFunc) CancelRequest(r *http.Request) {}

// Instances of this type are used to mock the client callbacks in unit tests.
type testCallback struct {
	success func(APIMessage)
	failure func(APIMessage, error)
}

func (c testCallback) Success(m APIMessage) {
	if c.success != nil {
		c.success(m)
	}
}

func (c testCallback) Failure(m APIMessage, e error) {
	if c.failure != nil {
		c.failure(m, e)
	}
}

// Instances of this type are used to mock the client logger in unit tests.
type testLogger struct {
	logf   func(string, ...interface{})
	errorf func(string, ...interface{})
}

func (l testLogger) Logf(format string, args ...interface{}) {
	if l.logf != nil {
		l.logf(format, args...)
	}
}

func (l testLogger) Errorf(format string, args ...interface{}) {
	if l.errorf != nil {
		l.errorf(format, args...)
	}
}

var _ Message = (*testErrorMessage)(nil)

// Instances of this type are used to force message validation errors in unit
// tests.
type testErrorMessage struct{}
type testAPIErrorMessage struct{}

func (m testErrorMessage) internal() {
}

func (m testErrorMessage) Validate() error { return testError }

func (m testErrorMessage) APIfy() APIMessage {
	return testAPIErrorMessage{}
}

var (
	// A control error returned by mock functions to emulate a failure.
	//lint:ignore ST1012 variable name is fine :D
	testError = errors.New("test error")

	// HTTP transport that always succeeds.
	testTransportOK = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     http.StatusText(http.StatusOK),
			StatusCode: http.StatusOK,
			Proto:      r.Proto,
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			Body:       ioutil.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})

	// HTTP transport that sleeps for a little while and eventually succeeds.
	testTransportDelayed = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		time.Sleep(10 * time.Millisecond)
		return testTransportOK.RoundTrip(r)
	})

	// HTTP transport that always returns a 400.
	testTransportBadRequest = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     http.StatusText(http.StatusBadRequest),
			StatusCode: http.StatusBadRequest,
			Proto:      r.Proto,
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			Body:       ioutil.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})

	// HTTP transport that always returns a 400 with an erroring body reader.
	testTransportBodyError = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     http.StatusText(http.StatusBadRequest),
			StatusCode: http.StatusBadRequest,
			Proto:      r.Proto,
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			Body:       ioutil.NopCloser(readFunc(func(b []byte) (int, error) { return 0, testError })),
			Request:    r,
		}, nil
	})

	// HTTP transport that always return an error.
	testTransportError = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, testError
	})
)

func fixture(name string) string {
	f, err := os.Open(filepath.Join("fixtures", name))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	b, err := ioutil.ReadAll(f)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func mockId() string { return "I'm unique" }

func mockTime() time.Time {
	// time.Unix(0, 0) fails on Circle
	return time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC)
}

func mockServer() (chan []byte, *httptest.Server) {
	done := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.NewBuffer(nil)
		io.Copy(buf, r.Body)

		var v interface{}
		err := json.Unmarshal(buf.Bytes(), &v)
		if err != nil {
			panic(err)
		}

		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			panic(err)
		}

		done <- b
	}))

	return done, server
}

func ExampleCapture() {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		uid:       mockId,
	})
	defer client.Close()

	client.Enqueue(Capture{
		Event:      "Download",
		DistinctId: "123456",
		Properties: Properties{
			"application": "PostHog Go",
			"version":     "1.0.0",
			"platform":    "macos", // :)
		},
		SendFeatureFlags: false,
	})

	fmt.Printf("%s\n", <-body)
	// Output:
	// {
	//   "api_key": "Csyjlnlun3OzyNJAafdlv",
	//   "batch": [
	//     {
	//       "distinct_id": "123456",
	//       "event": "Download",
	//       "library": "posthog-go",
	//       "library_version": "1.0.0",
	//       "properties": {
	//         "$lib": "posthog-go",
	//         "$lib_version": "1.0.0",
	//         "application": "PostHog Go",
	//         "platform": "macos",
	//         "version": "1.0.0"
	//       },
	//       "send_feature_flags": false,
	//       "timestamp": "2009-11-10T23:00:00Z",
	//       "type": "capture"
	//     }
	//   ]
	// }

}

func TestEnqueue(t *testing.T) {
	tests := map[string]struct {
		ref string
		msg Message
	}{
		"alias": {
			strings.TrimSpace(fixture("test-enqueue-alias.json")),
			Alias{Alias: "A", DistinctId: "B"},
		},

		"identify": {
			strings.TrimSpace(fixture("test-enqueue-identify.json")),
			Identify{
				DistinctId: "B",
				Properties: Properties{"email": "hey@posthog.com"},
			},
		},

		"groupIdentify": {
			strings.TrimSpace(fixture("test-enqueue-group-identify.json")),
			GroupIdentify{
				DistinctId: "$organization_id:5",
				Type:       "organization",
				Key:        "id:5",
				Properties: Properties{},
			},
		},

		"capture": {
			strings.TrimSpace(fixture("test-enqueue-capture.json")),
			Capture{
				Event:      "Download",
				DistinctId: "123456",
				Properties: Properties{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos", // :)
				},
				SendFeatureFlags: false,
			},
		},
		"*alias": {
			strings.TrimSpace(fixture("test-enqueue-alias.json")),
			&Alias{Alias: "A", DistinctId: "B"},
		},

		"*identify": {
			strings.TrimSpace(fixture("test-enqueue-identify.json")),
			&Identify{
				DistinctId: "B",
				Properties: Properties{"email": "hey@posthog.com"},
			},
		},

		"*groupIdentify": {
			strings.TrimSpace(fixture("test-enqueue-group-identify.json")),
			&GroupIdentify{
				DistinctId: "$organization_id:5",
				Type:       "organization",
				Key:        "id:5",
				Properties: Properties{},
			},
		},

		"*capture": {
			strings.TrimSpace(fixture("test-enqueue-capture.json")),
			&Capture{
				Event:      "Download",
				DistinctId: "123456",
				Properties: Properties{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos", // :)
				},
				SendFeatureFlags: false,
			},
		},
	}

	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint:  server.URL,
		Verbose:   true,
		Logger:    t,
		BatchSize: 1,
		now:       mockTime,
		uid:       mockId,
	})
	defer client.Close()

	for name, test := range tests {
		if err := client.Enqueue(test.msg); err != nil {
			t.Error(err)
			return
		}

		if res := string(<-body); res != test.ref {
			t.Errorf("%s: invalid response:\n- expected %s\n- received: %s", name, test.ref, res)
		}
	}
}

var _ Message = (*customMessage)(nil)

type customMessage struct {
}
type customAPIMessage struct {
}

func (c *customMessage) internal() {
}

func (c *customMessage) Validate() error {
	return nil
}

func (c *customMessage) APIfy() APIMessage {
	return customAPIMessage{}
}

func TestEnqueuingCustomTypeFails(t *testing.T) {
	client := New("0123456789")
	err := client.Enqueue(&customMessage{})

	if err.Error() != "messages with custom types cannot be enqueued: *posthog.customMessage" {
		t.Errorf("invalid/missing error when queuing unsupported message: %v", err)
	}
}

func TestCaptureWithInterval(t *testing.T) {
	const interval = 100 * time.Millisecond
	var ref = strings.TrimSpace(fixture("test-interval-capture.json"))

	body, server := mockServer()
	defer server.Close()

	t0 := time.Now()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint: server.URL,
		Interval: interval,
		Verbose:  true,
		Logger:   t,
		now:      mockTime,
		uid:      mockId,
	})
	defer client.Close()

	client.Enqueue(Capture{
		Event:      "Download",
		DistinctId: "123456",
		Properties: Properties{
			"application": "PostHog Go",
			"version":     "1.0.0",
			"platform":    "macos", // :)
		},
		SendFeatureFlags: false,
	})

	// Will flush in 100 milliseconds
	if res := string(<-body); ref != res {
		t.Errorf("invalid response:\n- expected %s\n- received: %s", ref, res)
	}

	if t1 := time.Now(); t1.Sub(t0) < interval {
		t.Error("the flushing interval is too short:", interval)
	}
}

func TestCaptureWithTimestamp(t *testing.T) {
	var ref = strings.TrimSpace(fixture("test-timestamp-capture.json"))

	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint:  server.URL,
		Verbose:   true,
		Logger:    t,
		BatchSize: 1,
		now:       mockTime,
		uid:       mockId,
	})
	defer client.Close()

	client.Enqueue(Capture{
		Event:      "Download",
		DistinctId: "123456",
		Properties: Properties{
			"application": "PostHog Go",
			"version":     "1.0.0",
			"platform":    "macos", // :)
		},
		SendFeatureFlags: false,
		Timestamp:        time.Date(2015, time.July, 10, 23, 0, 0, 0, time.UTC),
	})

	if res := string(<-body); ref != res {
		t.Errorf("invalid response:\n- expected %s\n- received: %s", ref, res)
	}
}

func TestCaptureMany(t *testing.T) {
	var ref = strings.TrimSpace(fixture("test-many-capture.json"))

	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint:  server.URL,
		Verbose:   true,
		Logger:    t,
		BatchSize: 3,
		now:       mockTime,
		uid:       mockId,
	})
	defer client.Close()

	for i := 0; i < 5; i++ {
		client.Enqueue(Capture{
			Event:      "Download",
			DistinctId: "123456",
			Properties: Properties{
				"application": "PostHog Go",
				"version":     i,
			},
			SendFeatureFlags: false,
		})
	}

	if res := string(<-body); ref != res {
		t.Errorf("invalid response:\n- expected %s\n- received: %s", ref, res)
	}
}

func TestClientCloseTwice(t *testing.T) {
	client := New("0123456789")

	if err := client.Close(); err != nil {
		t.Error("closing a client should not a return an error")
	}

	if err := client.Close(); err != ErrClosed {
		t.Error("closing a client a second time should return ErrClosed:", err)
	}

	if err := client.Enqueue(Capture{DistinctId: "1", Event: "A"}); err != ErrClosed {
		t.Error("using a client after it was closed should return ErrClosed:", err)
	}
}

func TestClientConfigError(t *testing.T) {
	client, err := NewWithConfig("0123456789", Config{
		Interval: -1 * time.Second,
	})

	if err == nil {
		t.Error("no error returned when creating a client with an invalid config")
	}

	if _, ok := err.(ConfigError); !ok {
		t.Errorf("invalid error type returned when creating a client with an invalid config: %T", err)
	}

	if client != nil {
		t.Error("invalid non-nil client object returned when creating a client with and invalid config:", client)
		client.Close()
	}
}

func TestClientEnqueueError(t *testing.T) {
	client := New("0123456789")
	defer client.Close()

	if err := client.Enqueue(testErrorMessage{}); err != testError {
		t.Error("invlaid error returned when queueing an invalid message:", err)
	}
}

func TestClientCallback(t *testing.T) {
	reschan := make(chan bool, 1)
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			func(m APIMessage) { reschan <- true },
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: testTransportOK,
	})

	client.Enqueue(Capture{
		DistinctId: "A",
		Event:      "B",
	})
	client.Close()

	select {
	case <-reschan:
	case err := <-errchan:
		t.Error("failure callback triggered:", err)
	}
}

func TestClientFlush(t *testing.T) {
	cbChan := make(chan flushCBResult)

	client, _ := NewWithConfig("0123456789", Config{
		Transport: testTransportOK,

		// set explicitly in-case defaults change
		Interval:     5 * time.Second,
		BatchSize:    100,
		FlushMaxWait: 5 * time.Second,

		Callback: &flushCB{
			c: cbChan,
		},
	})

	client.Enqueue(Capture{
		DistinctId: "AFlush",
		Event:      "BFlush",
	})

	// Deferring since Close will cause a flush and we don't want that
	defer client.Close()

	// Wait for a bit, flush should not have occurred
	var received bool
	var result flushCBResult

	select {
	case result = <-cbChan:
		received = true
	case <-time.After(100 * time.Millisecond):
		break
	}

	if received {
		t.Fatalf("received data on callback channel before expected flush interval: %+v", result)
	}

	// Ask to flush
	err := client.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush() should not return an error: %s", err)
	}

	var timeout bool
	received = false

	// Flush should've occurred immediately
	select {
	case <-cbChan:
		received = true
	case <-time.After(time.Second):
		timeout = true
	}

	if !received {
		t.Error("Did not receive callback after flush")
	}

	if timeout {
		t.Error("Reached timeout waiting for callbacks after Flush()")
	}

}

type flushCBResult struct {
	APIMessage APIMessage
	Error      error
}

type flushCB struct {
	c chan flushCBResult
}

func (fc *flushCB) Success(msg APIMessage) {
	go func() {
		fc.c <- flushCBResult{
			APIMessage: msg,
		}
	}()
}

func (fc *flushCB) Failure(msg APIMessage, err error) {
	go func() {
		fc.c <- flushCBResult{
			APIMessage: msg,
			Error:      err,
		}
	}()
}

func TestClientMarshalMessageError(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: testTransportOK,
	})

	// Functions cannot be serializable, this should break the JSON marshaling
	// and trigger the failure callback.
	client.Enqueue(Capture{
		DistinctId: "A",
		Event:      "B",
		Properties: Properties{"invalid": func() {}},
	})
	client.Close()

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for unserializable message")

	} else if _, ok := err.(*json.UnsupportedTypeError); !ok {
		t.Errorf("invalid error type returned by unserializable message: %T", err)
	}
}

func TestClientNewRequestError(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Endpoint: "://localhost:80", // Malformed endpoint URL.
		Logger:   testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: testTransportOK,
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Close()

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for an invalid request")
	}
}

func TestClientRoundTripperError(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: testTransportError,
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Close()

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for an invalid request")

	} else if e, ok := err.(*url.Error); !ok {
		t.Errorf("invalid error returned by round tripper: %T: %s", err, err)

	} else if e.Err != testError {
		t.Errorf("invalid error returned by round tripper: %T: %s", e.Err, e.Err)
	}
}

func TestClientRetryError(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return nil, testError
		}),
		BatchSize:  1,
		RetryAfter: func(i int) time.Duration { return time.Millisecond },
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})

	// Each retry should happen ~1 millisecond, this should give enough time to
	// the test to trigger the failure callback.
	time.Sleep(50 * time.Millisecond)

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for a retry falure")

	} else if e, ok := err.(*url.Error); !ok {
		t.Errorf("invalid error returned by round tripper: %T: %s", err, err)

	} else if e.Err != testError {
		t.Errorf("invalid error returned by round tripper: %T: %s", e.Err, e.Err)
	}

	client.Close()
}

func TestClientResponse400(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		// This HTTP transport always return 400's.
		Transport: testTransportBadRequest,
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Close()

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for a 400 response")
	}
}

func TestClientResponseBodyError(t *testing.T) {
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			nil,
			func(m APIMessage, e error) { errchan <- e },
		},
		// This HTTP transport always return 400's with an erroring body.
		Transport: testTransportBodyError,
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Close()

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered for a 400 response")

	} else if err != testError {
		t.Errorf("invalid error returned by erroring response body: %T: %s", err, err)
	}
}

func TestClientMaxConcurrentRequests(t *testing.T) {
	reschan := make(chan bool, 1)
	errchan := make(chan error, 1)

	client, _ := NewWithConfig("0123456789", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			func(m APIMessage) { reschan <- true },
			func(m APIMessage, e error) { errchan <- e },
		},
		Transport: testTransportDelayed,
		// Only one concurreny request can be submitted, because the transport
		// introduces a short delay one of the uploads should fail.
		BatchSize:             1,
		maxConcurrentRequests: 1,
	})

	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Enqueue(Capture{DistinctId: "A", Event: "B"})
	client.Close()

	if _, ok := <-reschan; !ok {
		t.Error("one of the requests should have succeeded but the result channel was empty")
	}

	if err := <-errchan; err == nil {
		t.Error("failure callback not triggered after reaching the request limit")

	} else if err != ErrTooManyRequests {
		t.Errorf("invalid error returned by erroring response body: %T: %s", err, err)
	}
}

func TestFeatureFlagsWithNoPersonalApiKey(t *testing.T) {
	// silence Errorf by tossing them in channel and not reading back
	errchan := make(chan error, 1)
	defer close(errchan)

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Logger: testLogger{t.Logf, t.Logf},
		Callback: testCallback{
			func(m APIMessage) {},
			func(m APIMessage, e error) { errchan <- e },
		},
	})
	defer client.Close()

	receivedErrors := [4]error{}
	receivedErrors[0] = client.ReloadFeatureFlags()
	_, receivedErrors[1] = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "some key",
			DistinctId: "some id",
		},
	)
	_, receivedErrors[2] = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "some key",
			DistinctId: "some id",
		},
	)
	_, receivedErrors[3] = client.GetFeatureFlags()

	for _, receivedError := range receivedErrors {
		if receivedError == nil || receivedError.Error() != "specifying a PersonalApiKey is required for using feature flags" {
			t.Errorf("feature flags methods should return error without personal api key")
			return
		}
	}

}

func TestSimpleFlagOld(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("test-api-feature-flag.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isEnabled, checkErr := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "simpleFlag",
			DistinctId: "hey",
		},
	)

	if checkErr != nil || isEnabled != true {
		t.Errorf("simple flag with null rollout percentage should be on for everyone")
	}

	// flagValue, valueError := client.GetFeatureFlag("simpleFlag", "hey", false, Groups{}, NewProperties(), map[string]Properties{})
	// if valueError != nil || flagValue != true {
	// 	t.Errorf("simple flag with null rollout percentage should have value 'true'")
	// }
}

func TestSimpleFlagCalculation(t *testing.T) {
	isEnabled, err := checkIfSimpleFlagEnabled("a", "b", 42)
	if err != nil || !isEnabled {
		t.Errorf("calculation for a.b should succeed and be true")
	}

	isEnabled, err = checkIfSimpleFlagEnabled("a", "b", 40)
	if err != nil || isEnabled {
		t.Errorf("calculation for a.b should succeed and be false")
	}
}

func TestComplexFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("test-api-feature-flag.json")))
		} else if !strings.HasPrefix(r.URL.Path, "/batch") {
			t.Errorf("client called an endpoint it shouldn't have")
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isEnabled, checkErr := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "enabled-flag",
			DistinctId: "hey",
		},
	)

	if checkErr != nil || isEnabled != true {
		t.Errorf("flag listed in /decide/ response should be marked as enabled")
	}

	flagValue, valueErr := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "enabled-flag",
			DistinctId: "hey",
		},
	)

	if valueErr != nil || flagValue != true {
		t.Errorf("flag listed in /decide/ response should be true")
	}
}

func TestMultiVariateFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte("{}"))
		} else if !strings.HasPrefix(r.URL.Path, "/batch") {
			t.Errorf("client called an endpoint it shouldn't have")
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isEnabled, checkErr := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "multi-variate-flag",
			DistinctId: "hey",
		},
	)

	if checkErr != nil || isEnabled == false {
		t.Errorf("flag listed in /decide/ response should be marked as enabled")
	}

	flagValue, err := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "multi-variate-flag",
			DistinctId: "hey",
		},
	)

	if err != nil || flagValue != "hello" {
		t.Errorf("flag listed in /decide/ response should have value 'hello'")
	}
}

func TestDisabledFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte("{}"))
		} else if !strings.HasPrefix(r.URL.Path, "/batch") {
			t.Errorf("client called an endpoint it shouldn't have")
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isEnabled, checkErr := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "disabled-flag",
			DistinctId: "hey",
		},
	)

	if checkErr != nil || isEnabled == true {
		t.Errorf("flag listed in /decide/ response should be marked as disabled")
	}

	flagValue, err := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "disabled-flag",
			DistinctId: "hey",
		},
	)

	if err != nil || flagValue != false {
		t.Errorf("flag listed in /decide/ response should have value 'false'")
	}
}
