#!/bin/bash
# _scripts/check-regression.sh <current_ratio.txt> [baseline_ratio.txt]

# This script delegates to the Go-based regression checker.
# It ensures the tool is built and then runs it with appropriate thresholds.

set -e

# Path to the check-regression tool directory
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TOOL_DIR="$SCRIPT_DIR/../internal/tools/check-regression"

# Resolve absolute paths for all arguments before we cd
# This ensures they remain valid even after we change the working directory.
ABS_ARGS=()
for arg in "$@"; do
    if [[ "$arg" == -* ]]; then
        ABS_ARGS+=("$arg")
    elif [[ -f "$arg" ]]; then
        ABS_ARGS+=("$(cd "$(dirname "$arg")" && pwd)/$(basename "$arg")")
    else
        ABS_ARGS+=("$arg")
    fi
done

CURRENT="${ABS_ARGS[0]}"
BASELINE="${ABS_ARGS[1]}"

REGRESSION_FOUND=0

# --- 1. Absolute Regression Check (Ours vs Standard) ---
echo "Checking Absolute Performance (vs Standard Library)..."
ABS_STAT=$(mktemp)
# Use go tool benchstat
go tool benchstat "$CURRENT" > "$ABS_STAT"
(cd "$TOOL_DIR" && go run . -mode absolute -threshold 1.1 "$ABS_STAT") || REGRESSION_FOUND=1
rm "$ABS_STAT"

# --- 2. Relative Regression Check (Current vs Baseline) ---
if [ -n "$BASELINE" ]; then
    echo "Checking Relative Performance Delta (vs Baseline)..."
    REL_STAT=$(mktemp)
    go tool benchstat "$BASELINE" "$CURRENT" > "$REL_STAT"
    (cd "$TOOL_DIR" && go run . -mode relative -threshold 1.1 "$REL_STAT") || REGRESSION_FOUND=1
    rm "$REL_STAT"
fi

exit $REGRESSION_FOUND
