#!/bin/bash

set -e -u -o pipefail

# Base directory
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT/internal/benchmark"

TAGS="goregexp goregexpre coregex re2cgo re2wasm hyperscan pcre2cgo"
OUTPUT_DIR="$REPO_ROOT/_benchmark_results"
mkdir -p "$OUTPUT_DIR"

echo "Running benchmarks for engines: $TAGS"
echo "Results will be stored in: $OUTPUT_DIR"

# Run benchmarks for all engines
# Use -count 5 for statistical significance
go test -bench . -benchmem -tags "$TAGS" -count 5 > "$OUTPUT_DIR/cgo_engines.txt"

echo "Benchmark complete."

echo "Processing results using bench-compare..."
go run "./internal/tools/bench-compare" "$OUTPUT_DIR/cgo_engines.txt" > "$OUTPUT_DIR/cgo_engines.md"

echo "Benchmark report generated: $OUTPUT_DIR/cgo_engines.md"
cat "$OUTPUT_DIR/cgo_engines.md"
