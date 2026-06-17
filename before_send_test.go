package posthog

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func readBeforeSendBatch(t *testing.T, body <-chan []byte) map[string]interface{} {
	t.Helper()

	select {
	case payload := <-body:
		var batch map[string]interface{}
		require.NoError(t, json.Unmarshal(payload, &batch))
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for batch request")
		return nil
	}
}

func firstBeforeSendMessage(t *testing.T, batch map[string]interface{}) map[string]interface{} {
	t.Helper()

	messages, ok := batch["batch"].([]interface{})
	require.True(t, ok)
	require.Len(t, messages, 1)

	message, ok := messages[0].(map[string]interface{})
	require.True(t, ok)
	return message
}

func firstBeforeSendProperties(t *testing.T, batch map[string]interface{}) map[string]interface{} {
	t.Helper()

	message := firstBeforeSendMessage(t, batch)
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	return properties
}

func TestBeforeSendHooksRunInOrderAndModifyCapture(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		BeforeSend: func(msg Message) Message {
			capture, ok := msg.(Capture)
			require.True(t, ok)
			require.Equal(t, "capture", capture.Type)
			require.Equal(t, mockTime(), capture.Timestamp)
			capture.Properties["order"] = "config"
			return capture
		},
	})
	require.NoError(t, err)
	defer client.Close()

	client.BeforeSend(func(msg Message) Message {
		capture, ok := msg.(Capture)
		require.True(t, ok)
		capture.Properties["order"] = capture.Properties["order"].(string) + ",client"
		capture.Properties["added_by_hook"] = true
		return capture
	})

	require.NoError(t, client.Enqueue(Capture{
		DistinctId:       "user-123",
		Event:            "test-event",
		SendFeatureFlags: SendFeatureFlags(false),
	}))

	properties := firstBeforeSendProperties(t, readBeforeSendBatch(t, body))
	require.Equal(t, "config,client", properties["order"])
	require.Equal(t, true, properties["added_by_hook"])
}

func TestBeforeSendNilDropsMessageAndStopsHooks(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var secondHookCalls atomic.Int64
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
	})
	require.NoError(t, err)

	client.BeforeSend(func(Message) Message { return nil })
	client.BeforeSend(func(msg Message) Message {
		secondHookCalls.Add(1)
		return msg
	})

	require.NoError(t, client.Enqueue(Capture{
		DistinctId:       "user-123",
		Event:            "test-event",
		SendFeatureFlags: SendFeatureFlags(false),
	}))
	require.NoError(t, client.Close())

	require.Zero(t, requests.Load())
	require.Zero(t, secondHookCalls.Load())
}

func TestBeforeSendReceivesTypedExceptionBeforeAPIfy(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	var sawException atomic.Bool
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
	})
	require.NoError(t, err)
	defer client.Close()

	client.BeforeSend(func(msg Message) Message {
		exception, ok := msg.(Exception)
		require.True(t, ok)
		require.Len(t, exception.ExceptionList, 1)
		sawException.Store(true)
		if exception.Properties == nil {
			exception.Properties = NewProperties()
		}
		exception.Properties["seen_exception"] = true
		return exception
	})

	require.NoError(t, client.Enqueue(Exception{
		DistinctId: "user-123",
		Properties: NewProperties(),
		ExceptionList: []ExceptionItem{{
			Type:  "error type",
			Value: "error value",
		}},
	}))

	message := firstBeforeSendMessage(t, readBeforeSendBatch(t, body))
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	require.True(t, sawException.Load())
	require.Equal(t, "$exception", message["event"])
	require.Equal(t, true, properties["seen_exception"])
}

func TestBeforeSendPanicAndInvalidReturnKeepCurrentMessage(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
	})
	require.NoError(t, err)
	defer client.Close()

	client.BeforeSend(func(msg Message) Message {
		capture := msg.(Capture)
		capture.Properties["panic_leaked"] = true
		panic("boom")
	})
	client.BeforeSend(func(msg Message) Message {
		capture := msg.(Capture)
		capture.Properties["invalid_leaked"] = true
		capture.Event = ""
		return capture
	})
	client.BeforeSend(func(msg Message) Message {
		capture := msg.(Capture)
		capture.Properties["after_error"] = true
		return capture
	})

	require.NoError(t, client.Enqueue(Capture{
		DistinctId:       "user-123",
		Event:            "test-event",
		SendFeatureFlags: SendFeatureFlags(false),
	}))

	message := firstBeforeSendMessage(t, readBeforeSendBatch(t, body))
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "user-123", message["distinct_id"])
	require.Equal(t, true, properties["after_error"])
	require.Nil(t, properties["panic_leaked"])
	require.Nil(t, properties["invalid_leaked"])
}
