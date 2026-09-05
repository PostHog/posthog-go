package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
)

// This example implements posthog.FlagDefinitionCacheProvider with a shared directory,
// so it runs without extra dependencies. A real deployment shares definitions across
// hosts, so use Redis or something like it there.

const (
	// Must be longer than the polling interval.
	flagCacheLockTTL      = 30 * time.Second
	flagCachePollInterval = 3 * time.Second
)

// FileFlagCache shares flag definitions between processes on one machine through a
// directory: one file holds the definitions, another is the leader election lock.
type FileFlagCache struct {
	dir        string
	name       string
	instanceID string
}

// NewFileFlagCache creates a provider for the given cache directory. Instances that
// should share definitions pass the same directory and name.
func NewFileFlagCache(dir, name string) (*FileFlagCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileFlagCache{dir: dir, name: name, instanceID: uuid.NewString()}, nil
}

func (c *FileFlagCache) cachePath() string { return filepath.Join(c.dir, c.name+".json") }
func (c *FileFlagCache) lockPath() string  { return filepath.Join(c.dir, c.name+".lock") }

// ShouldFetchFlagDefinitions elects a single instance to poll PostHog by holding a lock
// that expires.
//
// Read-then-write is not atomic on a filesystem, so two instances starting at the same
// moment can both take the lock. Redis with a Lua script does this atomically.
func (c *FileFlagCache) ShouldFetchFlagDefinitions(_ context.Context) (bool, error) {
	holder, expiry, err := c.readLock()
	if err != nil {
		return false, err
	}

	switch {
	case holder == c.instanceID:
		// Already the leader: extend the lock.
	case holder == "":
	case time.Now().Before(expiry):
		return false, nil
	}

	return true, c.writeLock()
}

// GetFlagDefinitions returns the published definitions, or nil when there are none.
func (c *FileFlagCache) GetFlagDefinitions(_ context.Context) (*posthog.FlagDefinitionCacheData, error) {
	contents, err := os.ReadFile(c.cachePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var data posthog.FlagDefinitionCacheData
	if err := json.Unmarshal(contents, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// OnFlagDefinitionsReceived publishes definitions, writing to a temporary file and
// renaming it so that a reader never observes a half-written payload.
func (c *FileFlagCache) OnFlagDefinitionsReceived(_ context.Context, data posthog.FlagDefinitionCacheData) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(c.dir, c.name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	fmt.Printf("   [%s] published %d flag definitions to the shared cache\n", c.shortID(), len(data.Flags))
	return os.Rename(tmp.Name(), c.cachePath())
}

// Shutdown releases the lock if this instance holds it.
func (c *FileFlagCache) Shutdown(_ context.Context) error {
	holder, _, err := c.readLock()
	if err != nil || holder != c.instanceID {
		return err
	}
	if err := os.Remove(c.lockPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *FileFlagCache) readLock() (holder string, expiry time.Time, err error) {
	contents, err := os.ReadFile(c.lockPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", time.Time{}, nil
	} else if err != nil {
		return "", time.Time{}, err
	}

	parts := strings.Fields(string(contents))
	if len(parts) != 2 {
		return "", time.Time{}, nil
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, nil
	}
	return parts[0], time.Unix(0, nanos), nil
}

func (c *FileFlagCache) writeLock() error {
	expiry := time.Now().Add(flagCacheLockTTL).UnixNano()
	return os.WriteFile(c.lockPath(), []byte(fmt.Sprintf("%s %d", c.instanceID, expiry)), 0o644)
}

func (c *FileFlagCache) shortID() string { return c.instanceID[:8] }

// TestFlagDefinitionCache runs two clients that share one flag definition cache.
func TestFlagDefinitionCache(projectAPIKey, secretKey, endpoint string) {
	dir, err := os.MkdirTemp("", "posthog-flag-cache")
	if err != nil {
		fmt.Printf("❌ Could not create the cache directory: %v\n", err)
		return
	}
	defer os.RemoveAll(dir)

	fmt.Printf("Two instances sharing the cache in %s\n\n", dir)

	clients := make([]posthog.Client, 0, 2)
	caches := make([]*FileFlagCache, 0, 2)

	for i := 0; i < 2; i++ {
		cache, err := NewFileFlagCache(dir, "my-service-production")
		if err != nil {
			fmt.Printf("❌ Could not create the cache provider: %v\n", err)
			return
		}

		client, err := posthog.NewWithConfig(projectAPIKey, posthog.Config{
			Endpoint:                           endpoint,
			SecretKey:                          secretKey,
			DefaultFeatureFlagsPollingInterval: flagCachePollInterval,
			FlagDefinitionCacheProvider:        cache,
		})
		if err != nil {
			fmt.Printf("❌ Could not create the client: %v\n", err)
			return
		}

		clients = append(clients, client)
		caches = append(caches, cache)
		fmt.Printf("   instance %s started\n", cache.shortID())
	}

	defer func() {
		for i, client := range clients {
			if err := client.Close(); err != nil {
				fmt.Printf("   [%s] close: %v\n", caches[i].shortID(), err)
			}
		}
	}()

	time.Sleep(2 * flagCachePollInterval)

	fmt.Println("\nEvaluating a flag on both instances, entirely from the shared definitions:")
	for i, client := range clients {
		flags, err := client.GetAllFlags(posthog.FeatureFlagPayloadNoKey{
			DistinctId:          "distinct-id-of-your-user",
			OnlyEvaluateLocally: true,
		})
		if err != nil {
			fmt.Printf("   [%s] %v\n", caches[i].shortID(), err)
			continue
		}
		fmt.Printf("   [%s] evaluated %d flags locally\n", caches[i].shortID(), len(flags))
	}
}
