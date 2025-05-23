package posthog

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestConfigZeroValue(t *testing.T) {
	c := Config{}

	if err := c.validate(); err != nil {
		t.Error("validating the zero-value configuration failed:", err)
	}
}

func TestConfigInvalidInterval(t *testing.T) {
	c := Config{
		Interval: -1 * time.Second,
	}

	if err := c.validate(); err == nil {
		t.Error("no error returned when validating a malformed config")

	} else if e, ok := err.(ConfigError); !ok {
		t.Error("invalid error returned when checking a malformed config:", err)

	} else if e.Field != "Interval" || e.Value.(time.Duration) != (-1*time.Second) {
		t.Error("invalid field error reported:", e)
	}
}

func TestConfigInvalidBatchSize(t *testing.T) {
	c := Config{
		BatchSize: -1,
	}

	if err := c.validate(); err == nil {
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
