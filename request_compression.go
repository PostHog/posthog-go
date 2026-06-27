package posthog

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"

	"github.com/andybalholm/brotli"
)

var compressGzip = gzipCompress

func gzipCompress(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip compression: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

func deflateCompress(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("deflate compression: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("deflate close: %w", err)
	}
	return buf.Bytes(), nil
}

func brotliCompress(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	if _, err := bw.Write(raw); err != nil {
		return nil, fmt.Errorf("brotli compression: %w", err)
	}
	if err := bw.Close(); err != nil {
		return nil, fmt.Errorf("brotli close: %w", err)
	}
	return buf.Bytes(), nil
}
