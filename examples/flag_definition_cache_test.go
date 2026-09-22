package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileFlagCacheRoundTrip(t *testing.T) {
	cache, err := NewFileFlagCache(t.TempDir(), "definitions")
	require.NoError(t, err)
	ctx := context.Background()

	data, err := cache.GetFlagDefinitions(ctx)
	require.NoError(t, err)
	require.Nil(t, data)

	const payload = `{
		"flags": [],
		"future_metadata": {"large_integer": 9007199254740993}
	}`
	require.NoError(t, cache.OnFlagDefinitionsReceived(ctx, json.RawMessage(payload)))
	data, err = cache.GetFlagDefinitions(ctx)
	require.NoError(t, err)
	require.Equal(t, payload, string(data))
}
