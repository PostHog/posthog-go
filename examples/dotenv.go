package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvFile loads environment variables from a .env file if it exists.
// This follows the same pattern as the Python SDK for consistency.
// The .env file should contain lines in the format: KEY=value
// Lines starting with # are treated as comments and ignored.
func LoadEnvFile() error {
	return LoadEnvFileFromPath(".env")
}

// LoadEnvFileFromPath loads environment variables from the specified file path.
// If the file doesn't exist, this function returns nil (no error).
func LoadEnvFileFromPath(path string) error {
	// Get absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	// Check if file exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		// File doesn't exist, which is fine - just return without error
		return nil
	}

	// Open and read the file
	file, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value format
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				
				// Only set if not already set (like Python's setdefault)
				if os.Getenv(key) == "" {
					os.Setenv(key, value)
				}
			}
		}
	}

	return scanner.Err()
}