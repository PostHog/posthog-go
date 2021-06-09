package posthog

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const LONG_SCALE = 0xfffffffffffffff

type FeatureFlagsPoller struct {
	ticker                       *time.Ticker // periodic ticker
	loaded                       chan bool
	shutdown                     chan bool
	forceReload                  chan bool
	featureFlags                 []FeatureFlag
	personalApiKey               string
	projectApiKey                string
	Errorf                       func(format string, args ...interface{})
	Endpoint                     string
	http                         http.Client
	mutex                        sync.RWMutex
	fetchedFlagsSuccessfullyOnce bool
}

type FeatureFlag struct {
	Key               string `json:"key"`
	IsSimpleFlag      bool   `json:"is_simple_flag"`
	RolloutPercentage *uint8 `json:"rollout_percentage"`
}

type FeatureFlagsResponse struct {
	Results []FeatureFlag `json:"results"`
}

type DecideRequestData struct {
	ApiKey     string `json:"api_key"`
	DistinctId string `json:"distinct_id"`
}

type DecideResponse struct {
	FeatureFlags []string `json:"featureFlags"`
}

func newFeatureFlagsPoller(projectApiKey string, personalApiKey string, errorf func(format string, args ...interface{}), endpoint string, httpClient http.Client) *FeatureFlagsPoller {
	poller := FeatureFlagsPoller{
		ticker:                       time.NewTicker(5 * time.Second),
		loaded:                       make(chan bool),
		shutdown:                     make(chan bool),
		forceReload:                  make(chan bool),
		personalApiKey:               personalApiKey,
		projectApiKey:                projectApiKey,
		Errorf:                       errorf,
		Endpoint:                     endpoint,
		http:                         httpClient,
		mutex:                        sync.RWMutex{},
		fetchedFlagsSuccessfullyOnce: false,
	}

	go poller.run()
	return &poller
}

func (poller *FeatureFlagsPoller) run() {
	poller.fetchNewFeatureFlags()

	for {
		select {
		case <-poller.shutdown:
			close(poller.shutdown)
			close(poller.forceReload)
			close(poller.loaded)
			poller.ticker.Stop()
			return
		case <-poller.forceReload:
		case <-poller.ticker.C:
			poller.fetchNewFeatureFlags()
		}
	}
}

func (poller *FeatureFlagsPoller) fetchNewFeatureFlags() {
	personalApiKey := poller.personalApiKey
	requestData := []byte{}
	headers := [][2]string{{"Authorization", "Bearer " + personalApiKey + ""}}
	res, err := poller.request("GET", "api/feature_flag", requestData, headers)
	if err != nil || res.StatusCode != http.StatusOK {
		poller.Errorf("Unable to fetch feature flags", err)
	}
	defer res.Body.Close()
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		poller.Errorf("Unable to fetch feature flags", err)
		return
	}
	featureFlagsResponse := FeatureFlagsResponse{}
	err = json.Unmarshal([]byte(resBody), &featureFlagsResponse)
	if err != nil {
		poller.Errorf("Unable to unmarshal response from api/feature_flag", err)
		return
	}
	poller.mutex.Lock()
	if !poller.fetchedFlagsSuccessfullyOnce {
		poller.loaded <- true
	}
	poller.featureFlags = featureFlagsResponse.Results
	poller.mutex.Unlock()

}

func (poller *FeatureFlagsPoller) IsFeatureEnabled(key string, distinctId string, defaultResult bool) bool {
	featureFlags := poller.GetFeatureFlags()

	if len(featureFlags) < 1 {
		return defaultResult
	}

	featureFlag := FeatureFlag{Key: ""}

	// avoid using flag for conflicts with Golang's stdlib `flag`
	for _, storedFlag := range featureFlags {
		if key == storedFlag.Key {
			featureFlag = storedFlag
			break
		}
	}

	if featureFlag.Key == "" {
		return defaultResult
	}

	isFlagEnabledResponse := false

	if featureFlag.IsSimpleFlag {
		rolloutPercentage := uint8(100)
		if featureFlag.RolloutPercentage != nil {
			rolloutPercentage = *featureFlag.RolloutPercentage
		}
		isFlagEnabledResponse = poller.isSimpleFlagEnabled(key, distinctId, rolloutPercentage)
	} else {
		requestDataBytes, err := json.Marshal(DecideRequestData{
			ApiKey:     poller.projectApiKey,
			DistinctId: distinctId,
		})
		if err != nil {
			poller.Errorf("Unable to marshal decide endpoint request data")
		}
		res, err := poller.request("POST", "decide", requestDataBytes, [][2]string{})
		if err != nil || res.StatusCode != http.StatusOK {
			poller.Errorf("Error calling decide")
		}
		resBody, err := ioutil.ReadAll(res.Body)
		if err != nil {
			poller.Errorf("Error reading decide response")
		}
		defer res.Body.Close()
		decideResponse := DecideResponse{}
		err = json.Unmarshal([]byte(resBody), &decideResponse)
		if err != nil {
			poller.Errorf("Error parsing decide response")
		}
		for _, enabledFlag := range decideResponse.FeatureFlags {
			if key == enabledFlag {
				isFlagEnabledResponse = true
			}
		}
	}

	return isFlagEnabledResponse
}

func (poller *FeatureFlagsPoller) isSimpleFlagEnabled(key string, distinctId string, rolloutPercentage uint8) bool {
	hash := sha1.New()

	// `Write` expects bytes. If you have a string `s`,
	// use `[]byte(s)` to coerce it to bytes.
	hash.Write([]byte("" + key + "." + distinctId + ""))

	// This gets the finalized hash result as a byte
	// slice. The argument to `Sum` can be used to append
	// to an existing byte slice: it usually isn't needed.
	digest := hash.Sum(nil)
	hexString := fmt.Sprintf("%x\n", digest)[:15]

	value, err := strconv.ParseInt(hexString, 16, 64)
	if err != nil {
		poller.Errorf("Error converting string to int")
	}
	return (float64(value) / LONG_SCALE) <= float64(rolloutPercentage)/100
}

func (poller *FeatureFlagsPoller) GetFeatureFlags() []FeatureFlag {
	// ensure flags are loaded on the first call
	if !poller.fetchedFlagsSuccessfullyOnce {
		<-poller.loaded
	}

	poller.mutex.RLock()

	defer poller.mutex.RUnlock()

	return poller.featureFlags
}

func (poller *FeatureFlagsPoller) request(method string, endpoint string, requestData []byte, headers [][2]string) (*http.Response, error) {

	url := poller.Endpoint + "/" + endpoint + "/"
	req, err := http.NewRequest(method, url, bytes.NewReader(requestData))
	if err != nil {
		poller.Errorf("creating request - %s", err)
	}

	version := getVersion()

	req.Header.Add("User-Agent", "posthog-go (version: "+version+")")
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", fmt.Sprintf("%d", len(requestData)))

	for _, header := range headers {
		req.Header.Add(header[0], header[1])
	}

	res, err := poller.http.Do(req)

	if err != nil {
		poller.Errorf("sending request - %s", err)
	}

	return res, err
}

func (poller *FeatureFlagsPoller) ForceReload() {
	poller.forceReload <- true
}

func (poller *FeatureFlagsPoller) shutdownPoller() {
	fmt.Println("I've been called")
	poller.shutdown <- true
}