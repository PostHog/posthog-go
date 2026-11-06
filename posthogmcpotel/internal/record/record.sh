#!/bin/bash
# Records agentgateway's OTLP trace export for every MCP scenario in drive.sh
# into testdata/producers/agentgateway-<version>/<config>.otlp.json.
# Usage: AGENTGATEWAY_BIN=/path/to/agentgateway internal/record/record.sh
set -euo pipefail

RECORD_DIR=$(cd "$(dirname "$0")" && pwd)
MODULE_DIR=$(cd "$RECORD_DIR/../.." && pwd)
WORK=$(mktemp -d)
pids=()
stop() { kill ${pids[@]+"${pids[@]}"} 2>/dev/null || true; wait ${pids[@]+"${pids[@]}"} 2>/dev/null || true; pids=(); }
trap 'stop; rm -rf "$WORK"' EXIT

version=$("${AGENTGATEWAY_BIN:?set AGENTGATEWAY_BIN}" --version | sed -n 's/.*"version": "\(.*\)".*/\1/p')
out="$MODULE_DIR/testdata/producers/agentgateway-$version"
mkdir -p "$out"

(cd "$MODULE_DIR" && go build -o "$WORK/bin/" ./internal/record/cmd/...)
TOKEN=$("$WORK/bin/mintjwt" -key "$WORK/key.pem" -jwks "$WORK/jwks.json")
export TOKEN

for config in all-signals filtered; do
  sed -e "s#\${WORK}#$WORK#g" -e "s#\${RECORD_DIR}#$RECORD_DIR#g" \
    "$RECORD_DIR/gateway.$config.yaml" > "$WORK/gateway.yaml"
  sink_out="$WORK/otlp-$config"
  "$WORK/bin/stub" -name alpha -mcp 127.0.0.1:3101 -api 127.0.0.1:3201 & pids+=($!)
  "$WORK/bin/stub" -name beta -mcp 127.0.0.1:3102 & pids+=($!)
  "$WORK/bin/sink" -addr 127.0.0.1:4318 -out "$sink_out" > "$WORK/sink-$config.log" & pids+=($!)
  "$AGENTGATEWAY_BIN" -f "$WORK/gateway.yaml" > "$WORK/gateway-$config.log" 2>&1 & gateway=$!
  until curl -sf -o /dev/null http://127.0.0.1:15021/healthz/ready; do sleep 0.2; done

  "$RECORD_DIR/drive.sh" > "$WORK/drive-$config.log"

  # agentgateway exports in batches on a timer and does not flush on shutdown.
  for _ in $(seq 100); do [ -n "$(ls "$sink_out" 2>/dev/null)" ] && break; sleep 0.2; done
  sleep 1
  kill "$gateway"; wait "$gateway" || true
  files=("$sink_out"/*.json)
  if [ "${#files[@]}" -ne 1 ]; then
    echo "expected one OTLP request for $config, got ${#files[@]}" >&2
    exit 1
  fi
  cp "${files[0]}" "$out/$config.otlp.json"
  echo "recorded $out/$config.otlp.json ($(grep -c '"spanId"' "${files[0]}") spans)"
  stop
done
