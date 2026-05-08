#!/bin/bash
set -e

# Usage: _scripts/update-visualization.sh <history_dir> <data_dir>

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <history_dir> <data_dir>"
    exit 1
fi

HISTORY_DIR=$1
DATA_DIR=$2

# 1. Backfill missing .json files from .txt files
for txt in "$HISTORY_DIR"/*.txt; do
    json="${txt%.txt}.json"
    if [ ! -f "$json" ]; then
        echo "Processing $txt..."
        # Note: landscape-gen expects paths relative to project root
        go run internal/tools/landscape-gen/main.go "$txt" "$json" || echo "Warning: failed to process $txt"
    fi
done

# 2. Update data/landscape.json with the LATEST entry
LATEST_JSON=$(ls -v "$HISTORY_DIR"/*.json | tail -n 1)
if [ -f "$LATEST_JSON" ]; then
    cp "$LATEST_JSON" "$DATA_DIR/landscape.json"
    echo "Updated $DATA_DIR/landscape.json from $LATEST_JSON"
fi

# 3. Update data/history.json (Trends index)
go run internal/tools/history-gen/main.go "$HISTORY_DIR" "$DATA_DIR/history.json"
