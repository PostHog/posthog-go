# Contributing

This adapter builds against the checked-out Go SDK, not a separately published SDK dependency.

## Local checks

From the repository root:

```sh
go test -race ./sdk_compliance_adapter
python3 -m unittest discover -s sdk_compliance_adapter -p 'test_*.py'
```

Adapter tests use an available loopback port; set `COMPLIANCE_TEST_PORT` to reserve
a specific port. Run `make test` for the complete Go test suite.

## Compliance profiles

CI pins the reusable workflow at `03d972e49be84402c491324320b0a0f38c2ddc53`
and the harness image at `1.0.0` (contract 1.2). Each profile gets a separate report artifact:

| Profile | Dockerfile | Capture tests | Flag tests |
| --- | --- | ---: | ---: |
| v0-gzip | `Dockerfile` | 30 | 17 |
| v1-gzip | `Dockerfile.v1` | 95 | 17 |
| v1-deflate | `Dockerfile.v1.deflate` | 94 | 17 |
| v1-br | `Dockerfile.v1.br` | 94 | 17 |
| v1-zstd | `Dockerfile.v1.zstd` | 94 | 17 |

Both protocol suites include the non-UTC timestamp override test. `CAPTURE_MODE=v1`
selects `posthog.CaptureModeAnalyticsV1`; `COMPRESSION` selects the codec used when
`enable_compression:true`. Omitted compression retains the SDK default (none), and
explicit false always selects none. Each process advertises only its selected codec.
The extra V1 profiles replace the two gzip-only tests with one codec header test.
The harness does not decode deflate, Brotli or zstd bodies and returns an empty V1
results map for those requests. The SDK treats UUIDs absent from results as accepted
without firing callbacks, so the adapter cannot observe their completion. The three
codec header tests currently time out at `/flush` before reaching their assertions,
although the SDK emits the requested encodings. These are harness decoding and
adapter completion gaps, not failing codecs or passing header assertions. The
adapter's own codec tests decode actual SDK requests and verify delivery callbacks
and retries using UUID-keyed results.

### Public SDK mapping

- Capture uses `NewWithConfig` and `Enqueue(Capture)`. Timestamp input is parsed as
  `time.Time`; UTC normalization, UUID generation, batching and retries remain SDK-owned.
- Flags use `EvaluateFlags` with singleton `FlagKeys`, then snapshot `GetFlag`.
  SDK transport, response parsing, retries and deduplicated `$feature_flag_called`
  events are exercised. Each action waits for SDK exposure delivery callbacks before
  returning, so a subsequent mock reset cannot receive the previous action's events.
  No personal API key or local evaluator is configured, so each action evaluates
  remotely regardless of `force_remote`.
- `BeforeSend` observes the SDK-generated UUID without changing the event.
  Public `Callback` notifications track successful and terminally failed events;
  the transport passively records actual attempts, including encoded bodies.
- The Go client has no non-closing immediate flush. `/flush` waits for the configured
  SDK interval and terminal callbacks, bounded to 30 seconds (or request cancellation).
  It returns HTTP 504 rather than claiming completion on timeout, including when V1
  responses omit event UUIDs and the SDK supplies no callback. `events_flushed`
  counts successful callbacks during that wait. It does not close/recreate the client.
  Default adapter batching is one event / 20 ms, with explicit init options forwarded.
- Reset and reinit close the old client before clearing its observations. Actions are
  serialized; parallel test isolation is not supported.

### Known contract differences

Expected reports are 45/47 for V0 gzip, 110/112 for V1 gzip, and 108/111 for each
alternate V1 codec (the completion timeout above plus two flag failures).

All 17 flag tests remain selected. Two are expected to fail through the native SDK:

- `feature_flags.request_payload.disable_geoip_false_propagates_as_geoip_disable_false`:
  the SDK omits the false field on the wire.
- `feature_flags.request_payload.disable_geoip_omitted_defaults_to_false`:
  the documented server SDK default is true.

Dedicated AI capture is not supported. Compliance assertions remain advisory, but
`report-inventory` fails when a profile report is absent, truncated, or lacks the
expected suite counts and UTC/codec/flag cases. Inspect each profile's artifact for
actual failures; an advisory job conclusion or the shared workflow's PR comment
is not a complete multi-profile result.

## Run with Docker

From the repository root (choose any Dockerfile from the table):

```sh
docker network create test-network
docker build -f sdk_compliance_adapter/Dockerfile.v1.deflate -t posthog-go-adapter .
docker run -d --name sdk-adapter --network test-network posthog-go-adapter
docker run --rm --name test-harness --network test-network \
  ghcr.io/posthog/sdk-test-harness:1.0.0 \
  run --adapter-url http://sdk-adapter:8080 --mock-url http://test-harness:8081 \
  --sdk-type server --concurrency 1
docker stop sdk-adapter && docker rm sdk-adapter
docker network rm test-network
```

`docker compose up --build --abort-on-container-exit` from this directory runs V0
only; V1 has separate CI profiles. For native development, build with
`go build -o /tmp/posthog-go-adapter ./sdk_compliance_adapter`, then set `PORT`,
`CAPTURE_MODE` and `COMPRESSION` when launching. `/init` accepts HTTP mock URLs on
explicit ports at `127.0.0.1`, `localhost`, `::1`, or Docker's `test-harness` host.
Confirm both adapter and mock HTTP readiness before invoking the pinned suites.
