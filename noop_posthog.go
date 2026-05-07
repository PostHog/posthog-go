package posthog

import "context"

type noopClient struct {
	Config
}

var (
	emptyFlagValues            = map[string]interface{}{}
	emptyFeatureFlags          = []FeatureFlag{}
	emptyEvaluatedFlagRecords  = map[string]evaluatedFlagRecord{}
	noopFeatureFlagResult      = &FeatureFlagResult{Enabled: false}
	noopFeatureFlagEvaluations = &FeatureFlagEvaluations{flags: emptyEvaluatedFlagRecords}
)

func newNoopClient(config Config) Client {
	return &noopClient{Config: config}
}

func (c *noopClient) Enqueue(Message) error {
	return ErrSDKDisabled
}

func (c *noopClient) IsFeatureEnabled(FeatureFlagPayload) (interface{}, error) {
	return false, ErrSDKDisabled
}

func (c *noopClient) GetFeatureFlag(FeatureFlagPayload) (interface{}, error) {
	return false, ErrSDKDisabled
}

func (c *noopClient) GetFeatureFlagResult(FeatureFlagPayload) (*FeatureFlagResult, error) {
	return noopFeatureFlagResult, ErrSDKDisabled
}

func (c *noopClient) GetFeatureFlagPayload(FeatureFlagPayload) (string, error) {
	return "", ErrSDKDisabled
}

func (c *noopClient) GetRemoteConfigPayload(string) (string, error) {
	return "", ErrSDKDisabled
}

func (c *noopClient) GetAllFlags(FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	return emptyFlagValues, ErrSDKDisabled
}

func (c *noopClient) EvaluateFlags(EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	return noopFeatureFlagEvaluations, ErrSDKDisabled
}

func (c *noopClient) ReloadFeatureFlags() error {
	return ErrSDKDisabled
}

func (c *noopClient) GetFeatureFlags() ([]FeatureFlag, error) {
	return emptyFeatureFlags, ErrSDKDisabled
}

func (c *noopClient) Close() error {
	return ErrSDKDisabled
}

func (c *noopClient) CloseWithContext(context.Context) error {
	return ErrSDKDisabled
}
