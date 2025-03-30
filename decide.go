package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"
)

// decider defines the interface for making decide requests
type decider interface {
	makeDecideRequest(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (*DecideResponse, error)
}

// decideClient implements the decider interface
type decideClient struct {
	apiKey                    string
	endpoint                  string
	http                      http.Client
	featureFlagRequestTimeout time.Duration
	errorf                    func(format string, args ...interface{})
}

// newDecideClient creates a new decideClient
func newDecideClient(apiKey string, endpoint string, httpClient http.Client, featureFlagRequestTimeout time.Duration, errorf func(format string, args ...interface{})) *decideClient {
	return &decideClient{
		apiKey:                    apiKey,
		endpoint:                  endpoint,
		http:                      httpClient,
		featureFlagRequestTimeout: featureFlagRequestTimeout,
		errorf:                    errorf,
	}
}

// makeDecideRequest makes a request to the decide endpoint and deserializes the response
// into a DecideResponse struct.
func (d *decideClient) makeDecideRequest(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (*DecideResponse, error) {
	requestData := DecideRequestData{
		ApiKey:           d.apiKey,
		DistinctId:       distinctId,
		Groups:           groups,
		PersonProperties: personProperties,
		GroupProperties:  groupProperties,
	}

	requestDataBytes, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal decide endpoint request data: %v", err)
	}

	decideEndpoint := "decide/?v=3"
	url, err := url.Parse(d.endpoint + "/" + decideEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating url: %v", err)
	}

	req, err := http.NewRequest("POST", url.String(), bytes.NewReader(requestDataBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "posthog-go/"+Version)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), d.featureFlagRequestTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	res, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from /decide/: %d", res.StatusCode)
	}

	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from /decide/: %v", err)
	}

	var decideResponse DecideResponse
	err = json.Unmarshal(resBody, &decideResponse)
	if err != nil {
		return nil, fmt.Errorf("error parsing response from /decide/: %v", err)
	}

	return &decideResponse, nil
}
