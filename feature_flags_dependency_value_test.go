package posthog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckFlagDependencyValue(t *testing.T) {
	// String variant matches string exactly (case-sensitive)
	result, err := checkFlagDependencyValue("control", "control")
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue("Control", "Control")
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue("control", "Control")
	require.NoError(t, err)
	require.False(t, result)

	result, err = checkFlagDependencyValue("Control", "CONTROL")
	require.NoError(t, err)
	require.False(t, result)

	result, err = checkFlagDependencyValue("control", "test")
	require.NoError(t, err)
	require.False(t, result)

	// String variant matches boolean true (any variant is truthy)
	result, err = checkFlagDependencyValue(true, "control")
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue(true, "test")
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue(false, "control")
	require.NoError(t, err)
	require.False(t, result)

	// Boolean matches boolean exactly
	result, err = checkFlagDependencyValue(true, true)
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue(false, false)
	require.NoError(t, err)
	require.True(t, result)

	result, err = checkFlagDependencyValue(false, true)
	require.NoError(t, err)
	require.False(t, result)

	result, err = checkFlagDependencyValue(true, false)
	require.NoError(t, err)
	require.False(t, result)

	// Empty string doesn't match
	result, err = checkFlagDependencyValue(true, "")
	require.NoError(t, err)
	require.False(t, result)

	result, err = checkFlagDependencyValue("control", "")
	require.NoError(t, err)
	require.False(t, result)

	// Type mismatches
	result, err = checkFlagDependencyValue(123, "control")
	require.NoError(t, err)
	require.False(t, result)

	result, err = checkFlagDependencyValue("control", true)
	require.NoError(t, err)
	require.False(t, result)
}
