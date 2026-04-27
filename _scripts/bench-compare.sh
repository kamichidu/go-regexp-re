#!/bin/bash
set -e

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Resolve path if an argument is provided
INPUT_FILE=""
if [ $# -gt 0 ]; then
    INPUT_FILE="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
fi

# Run the Go tool
cd "$ROOT_DIR/internal/tools/bench-compare"
go run . "$INPUT_FILE"
