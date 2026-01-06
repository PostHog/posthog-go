package posthog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigZeroValue(t *testing.T) {
	c := Config{}

	if err := c.Validate(); err != nil {
		t.Error("validating the zero-value configuration failed:", err)
	}
}

func TestConfigInvalidInterval(t *testing.T) {
	c := Config{
		Interval: -1 * time.Second,
	}

	if err := c.Validate(); err == nil {
		t.Error("no error returned when validating a malformed config")

	} else if e, ok := err.(ConfigError); !ok {
		t.Error("invalid error returned when checking a malformed config:", err)

	} else if e.Field != "Interval" || e.Value.(time.Duration) != (-1*time.Second) {
		t.Error("invalid field error reported:", e)
	}
}

func TestConfig_MaxRetries(t *testing.T) {
	c := Config{}
	require.NoError(t, c.Validate())
	got := makeConfig(c)
	require.Equal(t, 10, got.maxAttempts)

	c.MaxRetries = Ptr[int](-1)
	require.ErrorContains(t, c.Validate(),
		"posthog.NewWithConfig: max retries out of range [0,9] (posthog.Config.MaxRetries: -1)")
	got = makeConfig(c)
	require.Equal(t, 10, got.maxAttempts)

	c.MaxRetries = Ptr[int](10)
	require.ErrorContains(t, c.Validate(),
		"posthog.NewWithConfig: max retries out of range [0,9] (posthog.Config.MaxRetries: 10)")
	got = makeConfig(c)
	require.Equal(t, 10, got.maxAttempts)

	c.MaxRetries = Ptr[int](5)
	require.NoError(t, c.Validate())
	got = makeConfig(c)
	require.Equal(t, 6, got.maxAttempts)
}

func TestConfigInvalidBatchSize(t *testing.T) {
	c := Config{
		BatchSize: -1,
	}

	if err := c.Validate(); err == nil {
		t.Error("no error returned when validating a malformed config")

	} else if e, ok := err.(ConfigError); !ok {
		t.Error("invalid error returned when checking a malformed config:", err)

	} else if e.Field != "BatchSize" || e.Value.(int) != -1 {
		t.Error("invalid field error reported:", e)
	}
}

func TestConfigGetDisableGeoIP(t *testing.T) {
	var (
		c  Config
		tv = true
		fv = false
	)
	require.True(t, c.GetDisableGeoIP())
	c.DisableGeoIP = &tv
	require.True(t, c.GetDisableGeoIP())
	c.DisableGeoIP = &fv
	require.False(t, c.GetDisableGeoIP())
}

func TestConfigCompression(t *testing.T) {
	// CompressionNone (0) should be valid
	c := Config{Compression: CompressionNone}
	require.NoError(t, c.Validate())

	// CompressionGzip (1) should be valid
	c = Config{Compression: CompressionGzip}
	require.NoError(t, c.Validate())

	// Values > CompressionGzip should be invalid
	c = Config{Compression: 2}
	err := c.Validate()
	require.Error(t, err)
	configErr, ok := err.(ConfigError)
	require.True(t, ok, "expected ConfigError")
	require.Equal(t, "Compression", configErr.Field)
	require.Equal(t, CompressionMode(2), configErr.Value)
	require.Contains(t, configErr.Reason, "invalid compression mode")

	// Higher invalid values should also fail
	c = Config{Compression: 255}
	err = c.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid compression mode")
}
