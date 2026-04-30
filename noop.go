package posthog

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

type noopClient struct {
	Config
	closed    atomic.Bool
	closeOnce sync.Once
}

func newNoopClient(config Config) Client {
	return &noopClient{Config: config}
}

func (c *noopClient) Enqueue(msg Message) error {
	if c.closed.Load() {
		return ErrClosed
	}

	msg = dereferenceMessage(msg)
	if msg == nil {
		return nil
	}
	if err := msg.Validate(); err != nil {
		return err
	}

	switch msg.(type) {
	case Alias, Identify, GroupIdentify, Capture, Exception:
		return nil
	default:
		return fmt.Errorf("messages with custom types cannot be enqueued: %T", msg)
	}
}

func (c *noopClient) IsFeatureEnabled(flagConfig FeatureFlagPayload) (interface{}, error) {
	if err := flagConfig.validate(); err != nil {
		return false, err
	}
	return false, nil
}

func (c *noopClient) GetFeatureFlag(flagConfig FeatureFlagPayload) (interface{}, error) {
	if err := flagConfig.validate(); err != nil {
		return false, err
	}
	return false, nil
}

func (c *noopClient) GetFeatureFlagResult(flagConfig FeatureFlagPayload) (*FeatureFlagResult, error) {
	if err := flagConfig.validate(); err != nil {
		return nil, err
	}
	return &FeatureFlagResult{Key: flagConfig.Key, Enabled: false}, nil
}

func (c *noopClient) GetFeatureFlagPayload(flagConfig FeatureFlagPayload) (string, error) {
	if _, err := c.GetFeatureFlagResult(flagConfig); err != nil {
		return "", err
	}
	return "", nil
}

func (c *noopClient) GetRemoteConfigPayload(string) (string, error) {
	return "", nil
}

func (c *noopClient) GetAllFlags(flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	if err := flagConfig.validate(); err != nil {
		return nil, err
	}
	return map[string]interface{}{}, nil
}

func (c *noopClient) ReloadFeatureFlags() error {
	return nil
}

func (c *noopClient) GetFeatureFlags() ([]FeatureFlag, error) {
	return []FeatureFlag{}, nil
}

func (c *noopClient) Close() error {
	return c.CloseWithContext(context.Background())
}

func (c *noopClient) CloseWithContext(context.Context) error {
	alreadyClosed := true
	c.closeOnce.Do(func() {
		alreadyClosed = false
		c.closed.Store(true)
	})
	if alreadyClosed {
		return ErrClosed
	}
	return nil
}
