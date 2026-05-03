#!/bin/bash
set -e

# Usage: _scripts/generate-landscape.sh <benchmark_output.txt> <sbl_registry.json> <output.json>

if [ "$#" -ne 3 ]; then
    echo "Usage: $0 <benchmark_output.txt> <sbl_registry.json> <output.json>"
    exit 1
fi

INPUT_FILE=$1
REGISTRY_FILE=$2
OUTPUT_FILE=$3

# Ensure we are in the project root
cd "$(dirname "$0")/.."

# Run the Go tool to convert text to JSON
go run internal/tools/landscape-gen/main.go "$INPUT_FILE" "$REGISTRY_FILE" > "$OUTPUT_FILE"

echo "Successfully generated $OUTPUT_FILE from $INPUT_FILE"
