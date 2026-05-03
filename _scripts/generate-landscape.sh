#!/bin/bash
set -e

# Usage: _scripts/generate-landscape.sh <benchmark_output.txt> <output.json>

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <benchmark_output.txt> <output.json>"
    exit 1
fi

INPUT_FILE=$1
OUTPUT_FILE=$2

# Ensure we are in the project root
cd "$(dirname "$0")/.."

# Run the Go tool to convert text to JSON using internal SBL definitions
go run internal/tools/landscape-gen/main.go "$INPUT_FILE" "$OUTPUT_FILE"

echo "Successfully generated $OUTPUT_FILE from $INPUT_FILE"
