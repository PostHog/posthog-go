# PostHog Go SDK Compliance Adapter

This adapter wraps the posthog-go SDK for compliance testing with the [PostHog SDK Test Harness](https://github.com/PostHog/posthog-sdk-test-harness).

## Running Tests

Tests run automatically in CI via GitHub Actions. See the test harness repo for details.

### Locally with Docker Compose

```bash
# From the posthog-go/sdk_compliance_adapter directory
docker-compose up --build --abort-on-container-exit
```

This will:
1. Build the Go SDK adapter
2. Pull the test harness image
3. Run all compliance tests
4. Show results

### Manually with Docker

```bash
# Create network
docker network create test-network

# Build and run adapter
docker build -f sdk_compliance_adapter/Dockerfile -t posthog-go-adapter .
docker run -d --name sdk-adapter --network test-network -p 8080:8080 posthog-go-adapter

# Run test harness
docker run --rm \
  --name test-harness \
  --network test-network \
  ghcr.io/posthog/sdk-test-harness:latest \
  run --adapter-url http://sdk-adapter:8080 --mock-url http://test-harness:8081

# Cleanup
docker stop sdk-adapter && docker rm sdk-adapter
docker network rm test-network
```

## Implementation

See [main.go](main.go) for the adapter implementation.

The adapter implements the standard SDK adapter interface defined in the [test harness CONTRACT](https://github.com/PostHog/posthog-sdk-test-harness/blob/main/CONTRACT.yaml).

## Documentation

For complete documentation, see:
- [PostHog SDK Test Harness](https://github.com/PostHog/posthog-sdk-test-harness)
- [Adapter Implementation Guide](https://github.com/PostHog/posthog-sdk-test-harness/blob/main/ADAPTER_GUIDE.md)
