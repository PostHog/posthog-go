# Contributing

This package contains the PostHog Go SDK compliance adapter used with the PostHog SDK Test Harness.

## Running tests

Tests run automatically in CI via GitHub Actions.

CI runs two jobs: `compliance` (capture v0, `Dockerfile`) and `compliance-v1`
(capture v1, `Dockerfile.v1`). The only difference between the images is the
`CAPTURE_MODE=v1` env var, which flips the adapter's `/health` capabilities and
selects `posthog.CaptureModeAnalyticsV1` at init. Both jobs pin the reusable
workflow to the 0.9.0 release commit and run the `0.9.0` harness image.

### Locally with Docker Compose

Run the full compliance suite from the `sdk_compliance_adapter` directory:

```bash
docker-compose up --build --abort-on-container-exit
```

This will:

1. Build the Go SDK adapters (v0 on `:8080`, v1 on `:8082`)
2. Pull the test harness image
3. Run the capture v0 compliance tests against the v0 adapter

> **Note:** `docker-compose` currently targets the v0 adapter only. The v1
> adapter image is built to verify it compiles, but v1 compliance tests run in
> CI via the separate `compliance-v1` workflow job. To run v1 locally, use the
> manual Docker instructions below with `Dockerfile.v1`.

### Manually with Docker

```bash
# Create network
docker network create test-network

# Build and run adapter (use Dockerfile.v1 to exercise capture v1)
docker build -f sdk_compliance_adapter/Dockerfile -t posthog-go-adapter .
docker run -d --name sdk-adapter --network test-network -p 8080:8080 posthog-go-adapter

# Run test harness
docker run --rm \
  --name test-harness \
  --network test-network \
  ghcr.io/posthog/sdk-test-harness:0.9.0 \
  run --adapter-url http://sdk-adapter:8080 --mock-url http://test-harness:8081

# Cleanup
docker stop sdk-adapter && docker rm sdk-adapter
docker network rm test-network
```
