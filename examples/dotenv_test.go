package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile(t *testing.T) {
	// Create a temporary .env file
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	
	envContent := `# This is a comment
POSTHOG_PROJECT_API_KEY=phc_test_key
POSTHOG_PERSONAL_API_KEY=phx_test_key
POSTHOG_ENDPOINT=https://app.posthog.com

# Another comment
SOME_OTHER_VAR=some_value
`

	err := os.WriteFile(envFile, []byte(envContent), 0644)
	require.NoError(t, err)

	// Change to the temp directory so .env file is found
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	// Clear any existing env vars
	os.Unsetenv("POSTHOG_PROJECT_API_KEY")
	os.Unsetenv("POSTHOG_PERSONAL_API_KEY")
	os.Unsetenv("POSTHOG_ENDPOINT")
	os.Unsetenv("SOME_OTHER_VAR")

	// Load the .env file
	err = LoadEnvFile()
	require.NoError(t, err)

	// Check that variables were loaded
	require.Equal(t, "phc_test_key", os.Getenv("POSTHOG_PROJECT_API_KEY"))
	require.Equal(t, "phx_test_key", os.Getenv("POSTHOG_PERSONAL_API_KEY"))
	require.Equal(t, "https://app.posthog.com", os.Getenv("POSTHOG_ENDPOINT"))
	require.Equal(t, "some_value", os.Getenv("SOME_OTHER_VAR"))
}

func TestLoadEnvFileFromPath(t *testing.T) {
	// Create a temporary .env file with specific name
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "custom.env")
	
	envContent := `TEST_VAR=test_value
ANOTHER_VAR=another_value`

	err := os.WriteFile(envFile, []byte(envContent), 0644)
	require.NoError(t, err)

	// Clear any existing env vars
	os.Unsetenv("TEST_VAR")
	os.Unsetenv("ANOTHER_VAR")

	// Load the custom .env file
	err = LoadEnvFileFromPath(envFile)
	require.NoError(t, err)

	// Check that variables were loaded
	require.Equal(t, "test_value", os.Getenv("TEST_VAR"))
	require.Equal(t, "another_value", os.Getenv("ANOTHER_VAR"))
}

func TestLoadEnvFileDoesntOverrideExisting(t *testing.T) {
	// Create a temporary .env file
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	
	envContent := `EXISTING_VAR=from_file`

	err := os.WriteFile(envFile, []byte(envContent), 0644)
	require.NoError(t, err)

	// Set an existing environment variable
	os.Setenv("EXISTING_VAR", "from_environment")

	// Load the .env file
	err = LoadEnvFileFromPath(envFile)
	require.NoError(t, err)

	// Check that existing env var was not overridden
	require.Equal(t, "from_environment", os.Getenv("EXISTING_VAR"))
}

func TestLoadEnvFileNonExistent(t *testing.T) {
	// Try to load a non-existent .env file
	err := LoadEnvFileFromPath("/non/existent/path/.env")
	require.NoError(t, err) // Should not error for non-existent files
}

func TestLoadEnvFileWithComments(t *testing.T) {
	// Create a temporary .env file with various comment styles
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	
	envContent := `# Header comment
VAR1=value1
# Mid comment
VAR2=value2

# Empty line above
VAR3=value3
# Comment at end`

	err := os.WriteFile(envFile, []byte(envContent), 0644)
	require.NoError(t, err)

	// Clear env vars
	os.Unsetenv("VAR1")
	os.Unsetenv("VAR2")
	os.Unsetenv("VAR3")

	// Load the .env file
	err = LoadEnvFileFromPath(envFile)
	require.NoError(t, err)

	// Check that only actual variables were loaded
	require.Equal(t, "value1", os.Getenv("VAR1"))
	require.Equal(t, "value2", os.Getenv("VAR2"))
	require.Equal(t, "value3", os.Getenv("VAR3"))
}

func TestLoadEnvFileWithSpaces(t *testing.T) {
	// Create a temporary .env file with spaces around values
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	
	envContent := `VAR_WITH_SPACES = value with spaces 
VAR_NO_SPACES=nospaces
  VAR_LEADING_SPACES=value  `

	err := os.WriteFile(envFile, []byte(envContent), 0644)
	require.NoError(t, err)

	// Clear env vars
	os.Unsetenv("VAR_WITH_SPACES")
	os.Unsetenv("VAR_NO_SPACES") 
	os.Unsetenv("VAR_LEADING_SPACES")

	// Load the .env file
	err = LoadEnvFileFromPath(envFile)
	require.NoError(t, err)

	// Check that spaces were trimmed appropriately
	require.Equal(t, "value with spaces", os.Getenv("VAR_WITH_SPACES"))
	require.Equal(t, "nospaces", os.Getenv("VAR_NO_SPACES"))
	require.Equal(t, "value", os.Getenv("VAR_LEADING_SPACES"))
}