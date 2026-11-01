#!/usr/bin/env bash
set -euo pipefail

DAEMON_ADDR="${1:-127.0.0.1:18080}"
OUTPUT_JSON="${2:-benchmark-results.json}"
HTML_REPORT="${3:-benchmark-report.html}"
COMPARE_JSON="${4:-}"

echo "=========================================================================="
echo "          Running Compute Automated Performance Benchmark Suite        "
echo "=========================================================================="

# Build latest benchmark binary with static linking so it can be copied into MicroVMs
mkdir -p bin
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/compute-bench ./cmd/compute-bench

BENCH_ARGS=(
    "--daemon" "${DAEMON_ADDR}"
    "--output" "${OUTPUT_JSON}"
    "--html" "${HTML_REPORT}"
    "--api-iterations" "50"
    "--lifecycle-runs" "2"
    "--concurrency" "10"
)

if [ -n "${COMPARE_JSON}" ] && [ -f "${COMPARE_JSON}" ]; then
    BENCH_ARGS+=("--compare" "${COMPARE_JSON}")
fi

./bin/compute-bench "${BENCH_ARGS[@]}"
