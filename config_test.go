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

func TestConfigValidateTrimsWhitespaceSensitiveFields(t *testing.T) {
	c := Config{
		Endpoint:       " \nhttps://app.posthog.com\t ",
		PersonalApiKey: " \ttest-personal-key\n ",
	}

	require.NoError(t, c.Validate())
	require.Equal(t, "https://app.posthog.com", c.Endpoint)
	require.Equal(t, "test-personal-key", c.PersonalApiKey)
}

func TestConfigEffectiveSecretKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		want   string
	}{
		{"neither", Config{}, ""},
		{"personal only", Config{PersonalApiKey: "phx_personal"}, "phx_personal"},
		{"secret only", Config{SecretKey: "phs_secret"}, "phs_secret"},
		{"secret precedence", Config{SecretKey: "phs_secret", PersonalApiKey: "phx_personal"}, "phs_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.config.effectiveSecretKey())
		})
	}
}

func TestConfigValidateTrimsSecretKey(t *testing.T) {
	c := Config{SecretKey: " \tphs_secret\n "}
	require.NoError(t, c.Validate())
	require.Equal(t, "phs_secret", c.SecretKey)
}

func TestMakeConfigDefaultsWhitespaceEndpoint(t *testing.T) {
	got := makeConfig(Config{Endpoint: " \n\t "})
	require.Equal(t, DefaultEndpoint, got.Endpoint)
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

func TestConfigBoolDefaults(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config, *bool)
		get  func(Config) bool
	}{
		{"DisableGeoIP", func(c *Config, value *bool) { c.DisableGeoIP = value }, func(c Config) bool { return c.GetDisableGeoIP() }},
		{"IsServer", func(c *Config, value *bool) { c.IsServer = value }, func(c Config) bool { return c.GetIsServer() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertConfigBoolDefaultTrue(t, tt.set, tt.get)
		})
	}
}

func assertConfigBoolDefaultTrue(t *testing.T, set func(*Config, *bool), get func(Config) bool) {
	t.Helper()
	var c Config
	tv := true
	fv := false
	require.True(t, get(c))
	set(&c, &tv)
	require.True(t, get(c))
	set(&c, &fv)
	require.False(t, get(c))
}

func TestConfigCompression(t *testing.T) {
	// gzip and none are valid on both capture modes; zstd/deflate/brotli are
	// v1-only (legacy /batch/ cannot decode them); unknown values are rejected.
	cases := []struct {
		name        string
		compression CompressionMode
		captureMode CaptureMode
		wantErr     string // reason substring; "" means valid
		wantValue   CompressionMode
	}{
		{"none legacy", CompressionNone, CaptureModeLegacy, "", 0},
		{"none v1", CompressionNone, CaptureModeAnalyticsV1, "", 0},
		{"gzip legacy", CompressionGzip, CaptureModeLegacy, "", 0},
		{"gzip v1", CompressionGzip, CaptureModeAnalyticsV1, "", 0},
		{"zstd legacy rejected", CompressionZstd, CaptureModeLegacy, "zstd compression requires CaptureModeAnalyticsV1", CompressionZstd},
		{"zstd v1 ok", CompressionZstd, CaptureModeAnalyticsV1, "", 0},
		{"deflate legacy rejected", CompressionDeflate, CaptureModeLegacy, "deflate compression requires CaptureModeAnalyticsV1", CompressionDeflate},
		{"deflate v1 ok", CompressionDeflate, CaptureModeAnalyticsV1, "", 0},
		{"brotli legacy rejected", CompressionBrotli, CaptureModeLegacy, "brotli compression requires CaptureModeAnalyticsV1", CompressionBrotli},
		{"brotli v1 ok", CompressionBrotli, CaptureModeAnalyticsV1, "", 0},
		{"unknown legacy", CompressionMode(255), CaptureModeLegacy, "invalid compression mode", CompressionMode(255)},
		{"unknown v1", CompressionMode(255), CaptureModeAnalyticsV1, "invalid compression mode", CompressionMode(255)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Compression: tc.compression, CaptureMode: tc.captureMode}
			err := c.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			configErr, ok := err.(ConfigError)
			require.True(t, ok, "expected ConfigError")
			require.Equal(t, "Compression", configErr.Field)
			require.Equal(t, tc.wantValue, configErr.Value)
			require.Contains(t, configErr.Reason, tc.wantErr)
		})
	}
}
