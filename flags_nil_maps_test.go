package posthog

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMakeFlagsRequestMarshalsNilMapsAsEmptyObjects(t *testing.T) {
	var body string
	client, err := newFlagsClient("phc_test", "http://127.0.0.1:9/flags/", http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"featureFlags":{},"featureFlagPayloads":{}}`)),
				Request:    r,
			}, nil
		}),
	}, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, `"groups":null`) || strings.Contains(body, `"person_properties":null`) {
		t.Fatalf("expected empty objects in flags body, got %s", body)
	}
	if !strings.Contains(body, `"groups":{}`) || !strings.Contains(body, `"person_properties":{}`) {
		t.Fatalf("expected empty objects in flags body, got %s", body)
	}
}
