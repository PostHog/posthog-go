package posthog

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func TestCompressionNone(t *testing.T) {
	var receivedContentType string
	var receivedContentEncoding string
	var receivedURL string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			receivedContentType = r.Header.Get("Content-Type")
			receivedContentEncoding = r.Header.Get("Content-Encoding")
			receivedURL = r.URL.String()
			receivedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionNone,
		BatchSize:   1, // Flush immediately
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "test-event",
	})
	require.NoError(t, err)
	client.Close()

	// Verify no compression query param
	require.Equal(t, "/batch/", receivedURL)
	// Verify Content-Type is application/json
	require.Equal(t, "application/json", receivedContentType)
	// Verify no Content-Encoding header
	require.Empty(t, receivedContentEncoding)
	// Verify body is valid JSON (not compressed)
	var batch map[string]interface{}
	err = json.Unmarshal(receivedBody, &batch)
	require.NoError(t, err, "body should be valid JSON")
	require.Contains(t, batch, "batch")
}

func TestCompressionGzip(t *testing.T) {
	var receivedContentType string
	var receivedContentEncoding string
	var receivedURL string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			receivedContentType = r.Header.Get("Content-Type")
			receivedContentEncoding = r.Header.Get("Content-Encoding")
			receivedURL = r.URL.String()
			receivedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionGzip,
		BatchSize:   1, // Flush immediately
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "test-event",
	})
	require.NoError(t, err)
	client.Close()

	// Verify compression query param is present
	require.Equal(t, "/batch/?compression=gzip", receivedURL)
	// Verify Content-Type is still application/json
	require.Equal(t, "application/json", receivedContentType)
	// Verify Content-Encoding is gzip
	require.Equal(t, "gzip", receivedContentEncoding)
	// Verify body is valid GZIP that decompresses to valid JSON
	require.True(t, len(receivedBody) > 0, "body should not be empty")
	// Check gzip magic bytes
	require.True(t, len(receivedBody) >= 2 && receivedBody[0] == 0x1f && receivedBody[1] == 0x8b,
		"body should start with gzip magic bytes")
}

func TestCompressionGzipDecompressesCorrectly(t *testing.T) {
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			receivedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionGzip,
		BatchSize:   1,
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-123",
		Event:      "compression-test",
		Properties: Properties{
			"key1": "value1",
			"key2": 42,
		},
	})
	require.NoError(t, err)
	client.Close()

	// Decompress the body
	gr, err := gzip.NewReader(strings.NewReader(string(receivedBody)))
	require.NoError(t, err, "should be valid gzip")
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	require.NoError(t, err, "should decompress successfully")

	// Verify decompressed data is valid JSON with expected structure
	var batch map[string]interface{}
	err = json.Unmarshal(decompressed, &batch)
	require.NoError(t, err, "decompressed body should be valid JSON")
	require.Contains(t, batch, "api_key")
	require.Contains(t, batch, "batch")
	require.Equal(t, "test-key", batch["api_key"])

	// Verify batch contains our event
	batchArray := batch["batch"].([]interface{})
	require.Len(t, batchArray, 1)
	event := batchArray[0].(map[string]interface{})
	require.Equal(t, "user-123", event["distinct_id"])
	require.Equal(t, "compression-test", event["event"])
}

func TestCompressionGzipReducesPayloadSize(t *testing.T) {
	var uncompressedSize int
	var compressedSize int
	var uncompressedReceived, compressedReceived bool

	// First capture uncompressed size
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			uncompressedSize = len(body)
			uncompressedReceived = true
			w.WriteHeader(200)
		}
	}))

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionNone,
		BatchSize:   1,
	})
	require.NoError(t, err)

	// Send a larger payload with repetitive data (compresses well)
	props := Properties{}
	for i := 0; i < 100; i++ {
		props[string(rune('a'+i%26))+string(rune('0'+i%10))] = "repetitive-value-that-should-compress-well"
	}
	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "large-event",
		Properties: props,
	})
	require.NoError(t, err)
	client.Close()
	server.Close()

	require.True(t, uncompressedReceived, "server should have received uncompressed request")
	require.True(t, uncompressedSize > 0, "uncompressed size should be > 0")

	// Now capture compressed size
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			compressedSize = len(body)
			compressedReceived = true
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err = NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionGzip,
		BatchSize:   1,
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "large-event",
		Properties: props,
	})
	require.NoError(t, err)
	client.Close()

	require.True(t, compressedReceived, "server should have received compressed request")
	require.True(t, compressedSize > 0, "compressed size should be > 0")

	// Verify compression actually reduces size
	require.True(t, compressedSize < uncompressedSize,
		"compressed size (%d) should be less than uncompressed size (%d)", compressedSize, uncompressedSize)

	t.Logf("Uncompressed: %d bytes, Compressed: %d bytes, Ratio: %.2f%%",
		uncompressedSize, compressedSize, float64(compressedSize)/float64(uncompressedSize)*100)
}

func TestCompressionModeConstants(t *testing.T) {
	// Verify constant values are as documented
	require.Equal(t, CompressionMode(0), CompressionNone)
	require.Equal(t, CompressionMode(1), CompressionGzip)
}

func TestCompressionGzipWithCallback(t *testing.T) {
	var requestReceived bool
	var successCount, failureCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			requestReceived = true
			// Verify it's gzip compressed
			require.Equal(t, "gzip", r.Header.Get("Content-Encoding"))
			require.Contains(t, r.URL.String(), "compression=gzip")
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionGzip,
		BatchSize:   1,
		Callback: testCallback{
			success: func(m APIMessage) { successCount++ },
			failure: func(m APIMessage, e error) { failureCount++ },
		},
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "test-event",
	})
	require.NoError(t, err)
	client.Close()

	// Verify request was received and callback reported success
	require.True(t, requestReceived, "server should have received request")
	require.Equal(t, 1, successCount, "should have 1 success callback")
	require.Equal(t, 0, failureCount, "should have 0 failure callbacks")
}

func TestCompressionNoneWithCallback(t *testing.T) {
	var requestReceived bool
	var successCount, failureCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/batch") {
			requestReceived = true
			// Verify it's NOT gzip compressed
			require.Empty(t, r.Header.Get("Content-Encoding"))
			require.NotContains(t, r.URL.String(), "compression=gzip")
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: CompressionNone,
		BatchSize:   1,
		Callback: testCallback{
			success: func(m APIMessage) { successCount++ },
			failure: func(m APIMessage, e error) { failureCount++ },
		},
	})
	require.NoError(t, err)

	err = client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "test-event",
	})
	require.NoError(t, err)
	client.Close()

	// Verify request was received and callback reported success
	require.True(t, requestReceived, "server should have received request")
	require.Equal(t, 1, successCount, "should have 1 success callback")
	require.Equal(t, 0, failureCount, "should have 0 failure callbacks")
}

