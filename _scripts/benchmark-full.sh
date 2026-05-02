#!/bin/bash

set -e -u -o pipefail

# Base directory
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT/internal/benchmark"

TAGS="goregexp goregexpre coregex re2cgo hyperscan pcre2cgo"
OUTPUT_DIR="$REPO_ROOT/_benchmark_results"
mkdir -p "$OUTPUT_DIR"

echo "Running benchmarks for engines: $TAGS"
echo "Results will be stored in: $OUTPUT_DIR"

# Run benchmarks for all engines
# Use -count 5 for statistical significance
go test -bench . -benchmem -tags "$TAGS" -count 5 > "$OUTPUT_DIR/cgo_engines.txt"

echo "Benchmark complete."
echo "Summary using benchstat (comparing GoRegexp vs GoRegexpRe):"
# Extract GoRegexp and GoRegexpRe comparison
go tool benchstat "$OUTPUT_DIR/cgo_engines.txt"
