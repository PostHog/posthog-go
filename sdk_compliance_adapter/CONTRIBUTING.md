# Contributing

This package contains the PostHog Go SDK compliance adapter used with the PostHog SDK Test Harness.

## Running tests

Tests run automatically in CI via GitHub Actions.

CI runs the `compliance` job once per codec (gzip, deflate, br, zstd) against the `1.13.1` harness image.
The harness has one `enable_compression` flag but a test per codec, so the adapter reads its codec from the `COMPRESSION` env var (default `gzip`).

The adapter advertises these capabilities on `/health`, which is how the harness selects tests:

- `capture_v1`: the analytics v1 suite (`/capture`).
- `capture_ai_v1`: the AI v1 suite (`/capture_ai`, sent with `EnqueueAI`).
- `event_options`: options are passed to the SDK unchanged.
- `encoding_<codec>`: the compression test for the adapter's codec.

### Locally with Docker Compose

Run the full compliance suite from the `sdk_compliance_adapter` directory:

```bash
docker-compose up --build --abort-on-container-exit
```

Set `COMPRESSION=zstd` (or `deflate`, `br`) in the environment to test another codec.

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
docker run -d --name sdk-adapter --network test-network -p 8080:8080 -e COMPRESSION=gzip posthog-go-adapter

# Run test harness
docker run --rm \
  --name test-harness \
  --network test-network \
  ghcr.io/posthog/sdk-test-harness:1.13.1 \
  run --adapter-url http://sdk-adapter:8080 --mock-url http://test-harness:8081

# Cleanup
docker stop sdk-adapter && docker rm sdk-adapter
docker network rm test-network
```
