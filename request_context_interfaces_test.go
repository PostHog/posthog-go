package posthog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestContextMiddleware_MixedOptionalInterfaces(t *testing.T) {
	base := &allOptionalResponseWriter{minimalResponseWriter: newMinimalResponseWriter()}
	cases := []struct {
		name   string
		writer http.ResponseWriter
	}{
		{"flush", struct {
			http.ResponseWriter
			http.Flusher
		}{base, base}},
		{"hijack", struct {
			http.ResponseWriter
			http.Hijacker
		}{base, base}},
		{"push", struct {
			http.ResponseWriter
			http.Pusher
		}{base, base}},
		{"read", struct {
			http.ResponseWriter
			io.ReaderFrom
		}{base, base}},
		{"flush+hijack", struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
		}{base, base, base}},
		{"flush+push", struct {
			http.ResponseWriter
			http.Flusher
			http.Pusher
		}{base, base, base}},
		{"flush+read", struct {
			http.ResponseWriter
			http.Flusher
			io.ReaderFrom
		}{base, base, base}},
		{"hijack+push", struct {
			http.ResponseWriter
			http.Hijacker
			http.Pusher
		}{base, base, base}},
		{"hijack+read", struct {
			http.ResponseWriter
			http.Hijacker
			io.ReaderFrom
		}{base, base, base}},
		{"push+read", struct {
			http.ResponseWriter
			http.Pusher
			io.ReaderFrom
		}{base, base, base}},
		{"flush+hijack+push", struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
			http.Pusher
		}{base, base, base, base}},
		{"flush+hijack+read", struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
			io.ReaderFrom
		}{base, base, base, base}},
		{"flush+push+read", struct {
			http.ResponseWriter
			http.Flusher
			http.Pusher
			io.ReaderFrom
		}{base, base, base, base}},
		{"hijack+push+read", struct {
			http.ResponseWriter
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{base, base, base, base}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*base = allOptionalResponseWriter{minimalResponseWriter: newMinimalResponseWriter()}
			want := captureOptionalInterfaceReport(tc.writer)
			handler := NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				require.Equal(t, want, captureOptionalInterfaceReport(w))
				if want.flusher {
					w.(http.Flusher).Flush()
				}
				if want.hijacker {
					_, _, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
				}
				if want.pusher {
					require.NoError(t, w.(http.Pusher).Push("/asset.css", nil))
				}
				if want.readerFrom {
					n, err := w.(io.ReaderFrom).ReadFrom(strings.NewReader("body"))
					require.NoError(t, err)
					require.Equal(t, int64(4), n)
				}
			}), WithCapturePanics(&noopClient{}))
			handler.ServeHTTP(tc.writer, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, want.flusher, base.flushed)
			require.Equal(t, want.hijacker, base.hijacked)
			require.Equal(t, want.readerFrom, base.readFromCalled)
			if want.pusher {
				require.Equal(t, "/asset.css", base.pushedTarget)
			}
			if want.readerFrom {
				require.Equal(t, "body", base.body.String())
			}
		})
	}
}

func TestResponseStatusWriterWriteCommitsOK(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := newResponseStatusWriter(recorder)
	n, err := writer.Write([]byte("body"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	writer.WriteHeader(http.StatusInternalServerError)
	require.Equal(t, http.StatusOK, writer.statusCode)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "body", recorder.Body.String())
}
