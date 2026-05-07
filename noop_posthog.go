package posthog

import "context"

type noopClient struct {
	Config
}

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

func (c *noopClient) GetFeatureFlagResult(flagConfig FeatureFlagPayload) (*FeatureFlagResult, error) {
	return &FeatureFlagResult{Key: flagConfig.Key, Enabled: false}, nil
}

func (c *noopClient) GetFeatureFlagPayload(FeatureFlagPayload) (string, error) {
	return "", nil
}

func (c *noopClient) GetRemoteConfigPayload(string) (string, error) {
	return "", nil
}

func (c *noopClient) GetAllFlags(FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (c *noopClient) EvaluateFlags(payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	return &FeatureFlagEvaluations{
		distinctId: payload.DistinctId,
		deviceId:   payload.DeviceId,
		groups:     payload.Groups,
		flags:      map[string]evaluatedFlagRecord{},
		accessed:   map[string]struct{}{},
	}, nil
}

func (c *noopClient) ReloadFeatureFlags() error {
	return nil
}

func (c *noopClient) GetFeatureFlags() ([]FeatureFlag, error) {
	return []FeatureFlag{}, nil
}

func (c *noopClient) Close() error {
	return nil
}

func (c *noopClient) CloseWithContext(context.Context) error {
	return nil
}
