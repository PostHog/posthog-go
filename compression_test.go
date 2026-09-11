package posthog

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Per-codec wire behavior (Content-Encoding header, body round-trip, success
// callbacks) and the compress-failure fallback are covered end-to-end by
// TestV1SendCompressionCodecs, TestV1SendCompressionFailureFallsBackToUncompressed
// and TestCompressV1Body* in capture_v1_send_test.go. What remains here is the
// coverage those do not provide: the documented constant values, and proof that
// compression actually shrinks a real request.

func TestCompressionModeConstants(t *testing.T) {
	// Verify constant values are as documented (wire-stable: external callers
	// may persist these as ints).
	require.Equal(t, CompressionMode(0), CompressionNone)
	require.Equal(t, CompressionMode(1), CompressionGzip)
	require.Equal(t, CompressionMode(2), CompressionZstd)
	require.Equal(t, CompressionMode(3), CompressionDeflate)
	require.Equal(t, CompressionMode(4), CompressionBrotli)
}

// sendAndMeasure enqueues one event with the given compression mode and returns
// the number of bytes the server actually received. A gzip body is decompressed
// before it is acknowledged, so the request is also proven to round-trip.
func sendAndMeasure(t *testing.T, mode CompressionMode, props Properties) int {
	t.Helper()

	var wireBytes int
	received := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		wireBytes = len(body)

		decoded := body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			require.NoError(t, err)
			decoded, err = io.ReadAll(zr)
			require.NoError(t, err)
			require.NoError(t, zr.Close())
		}
		writeCaptureOK(w, decoded)
		close(received)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:    server.URL,
		Compression: mode,
		BatchSize:   1,
	})
	require.NoError(t, err)

	require.NoError(t, client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "large-event",
		Properties: props,
	}))
	require.NoError(t, client.Close())

	<-received
	require.Greater(t, wireBytes, 0, "server should have received a non-empty body")
	return wireBytes
}

func TestCompressionGzipReducesPayloadSize(t *testing.T) {
	// Repetitive data so the ratio is unambiguous regardless of gzip tuning.
	props := Properties{}
	for i := 0; i < 100; i++ {
		props[string(rune('a'+i%26))+string(rune('0'+i%10))] = "repetitive-value-that-should-compress-well"
	}

	uncompressed := sendAndMeasure(t, CompressionNone, props)
	compressed := sendAndMeasure(t, CompressionGzip, props)

	require.Less(t, compressed, uncompressed,
		"compressed size (%d) should be less than uncompressed size (%d)", compressed, uncompressed)
	t.Logf("Uncompressed: %d bytes, Compressed: %d bytes, Ratio: %.2f%%",
		uncompressed, compressed, float64(compressed)/float64(uncompressed)*100)
}
