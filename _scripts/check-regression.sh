#!/bin/bash
# _scripts/check-regression.sh <current_ratio.txt> [baseline_ratio.txt]

# This script delegates to the Go-based regression checker.
# It ensures the tool is built and then runs it with appropriate thresholds.

set -e

# Path to the check-regression tool directory
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TOOL_DIR="$SCRIPT_DIR/../internal/tools/check-regression"

# Resolve absolute paths for all arguments before we cd
ABS_ARGS=()
for arg in "$@"; do
    if [[ "$arg" == -* ]]; then
        ABS_ARGS+=("$arg")
    elif [[ -e "$arg" ]]; then
        ABS_ARGS+=("$(cd "$(dirname "$arg")" && pwd)/$(basename "$arg")")
    else
        ABS_ARGS+=("$arg")
    fi
done

REGRESSION_FOUND=0

if [ "${#ABS_ARGS[@]}" -eq 1 ]; then
    # --- Mode 1: Absolute Regression Check (Ours vs Standard) ---
    CURRENT="${ABS_ARGS[0]}"
    echo "Checking Absolute Performance (vs Standard Library)..."
    ABS_STAT=$(mktemp)
    go tool benchstat "$CURRENT" > "$ABS_STAT"
    (cd "$TOOL_DIR" && go run . -mode absolute -threshold 1.1 "$ABS_STAT") || REGRESSION_FOUND=1
    rm "$ABS_STAT"
elif [ "${#ABS_ARGS[@]}" -eq 2 ]; then
    # --- Mode 2: Relative Regression Check (Current vs Baseline) ---
    CURRENT="${ABS_ARGS[0]}"
    BASELINE="${ABS_ARGS[1]}"
    echo "Checking Relative Performance Delta (vs Baseline)..."
    REL_STAT=$(mktemp)
    # Note: benchstat takes baseline as first file, current as second file.
    go tool benchstat "$BASELINE" "$CURRENT" > "$REL_STAT"
    (cd "$TOOL_DIR" && go run . -mode relative -threshold 1.1 "$REL_STAT") || REGRESSION_FOUND=1
    rm "$REL_STAT"
else
    echo "Usage: $0 <current_ratio.txt> [baseline_ratio.txt]"
    exit 1
fi

exit $REGRESSION_FOUND
