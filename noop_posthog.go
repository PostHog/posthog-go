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
	return nil
}

func (c *noopClient) IsFeatureEnabled(FeatureFlagPayload) (interface{}, error) {
	return false, nil
}

func (c *noopClient) GetFeatureFlag(FeatureFlagPayload) (interface{}, error) {
	return false, nil
}

func (c *noopClient) GetFeatureFlagResult(FeatureFlagPayload) (*FeatureFlagResult, error) {
	return noopFeatureFlagResult, nil
}

func (c *noopClient) GetFeatureFlagPayload(FeatureFlagPayload) (string, error) {
	return "", nil
}

func (c *noopClient) GetRemoteConfigPayload(string) (string, error) {
	return "", nil
}

func (c *noopClient) GetAllFlags(FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	return emptyFlagValues, nil
}

func (c *noopClient) EvaluateFlags(EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	return noopFeatureFlagEvaluations, nil
}

func (c *noopClient) ReloadFeatureFlags() error {
	return nil
}

func (c *noopClient) GetFeatureFlags() ([]FeatureFlag, error) {
	return emptyFeatureFlags, nil
}

func (c *noopClient) Close() error {
	return nil
}

func (c *noopClient) CloseWithContext(context.Context) error {
	return nil
}
