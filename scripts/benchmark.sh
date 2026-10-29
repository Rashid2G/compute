#!/usr/bin/env bash
set -euo pipefail

DAEMON_ADDR="${1:-127.0.0.1:18080}"
OUTPUT_JSON="${2:-benchmark-results.json}"
COMPARE_JSON="${3:-}"

echo "=========================================================================="
echo "          Running MicroForge Automated Performance Benchmark Suite        "
echo "=========================================================================="

# Build latest benchmark binary
mkdir -p bin
go build -o bin/mforge-bench ./cmd/mforge-bench

BENCH_ARGS=(
    "--daemon" "${DAEMON_ADDR}"
    "--output" "${OUTPUT_JSON}"
    "--api-iterations" "50"
    "--lifecycle-runs" "2"
)

if [ -n "${COMPARE_JSON}" ] && [ -f "${COMPARE_JSON}" ]; then
    BENCH_ARGS+=("--compare" "${COMPARE_JSON}")
fi

./bin/mforge-bench "${BENCH_ARGS[@]}"
