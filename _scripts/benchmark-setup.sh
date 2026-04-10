#!/bin/bash

set -e -u -o pipefail

echo "Installing CGO engine dependencies (requires sudo)..."
sudo apt-get update
sudo apt-get install -y \
    pkg-config \
    libre2-dev \
    libhyperscan-dev \
    libpcre2-dev

echo "Setup complete."
