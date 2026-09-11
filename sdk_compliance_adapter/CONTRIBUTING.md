# Contributing

This package contains the PostHog Go SDK compliance adapter used with the PostHog SDK Test Harness.

## Running tests

Tests run automatically in CI via GitHub Actions.

CI runs a single `compliance` job. The adapter advertises the `capture_v1`
capability on `/health`, which is how the harness selects its
`capture_analytics_v1` suite. The job pins the reusable workflow to the 0.10.0
release commit and runs the `0.10.0` harness image.

### Locally with Docker Compose

Run the full compliance suite from the `sdk_compliance_adapter` directory:

```bash
docker-compose up --build --abort-on-container-exit
```

This will:

1. Build the Go SDK adapter on `:8080`
2. Pull the test harness image
3. Run the capture compliance tests against the adapter

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
  ghcr.io/posthog/sdk-test-harness:0.10.0 \
  run --adapter-url http://sdk-adapter:8080 --mock-url http://test-harness:8081

# Cleanup
docker stop sdk-adapter && docker rm sdk-adapter
docker network rm test-network
```
