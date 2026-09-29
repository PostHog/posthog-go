package extras

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTimeoutTransportBodyLifetime(t *testing.T) {
	var requestContext context.Context
	transport := &timeoutTransport{
		timeout: time.Minute,
		rt: transportFunc(func(r *http.Request) (*http.Response, error) {
			requestContext = r.Context()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body"))}, nil
		}),
	}
	req, err := http.NewRequest(http.MethodGet, "http://posthog.test", nil)
	require.NoError(t, err)
	res, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.NoError(t, requestContext.Err(), "response body must remain readable after headers arrive")
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "body", string(body))
	require.NoError(t, res.Body.Close())
	require.ErrorIs(t, requestContext.Err(), context.Canceled)
}
