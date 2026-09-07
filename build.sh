#!/usr/bin/env bash
set -e

echo "🔨 [1/3] Compiling Go production binaries (-s -w)..."
mkdir -p bin dist
go build -ldflags="-s -w" -o bin/hydra-daemon main.go
go build -ldflags="-s -w" -o bin/hydra cmd/hydra-cli/main.go
go build -ldflags="-s -w" -o bin/hydra-tui ./cmd/hydra-tui

echo "📦 [2/3] Building .deb and .rpm packages with nfpm..."
nfpm package --config nfpm.yaml --packager deb --target dist/
nfpm package --config nfpm.yaml --packager rpm --target dist/

echo "✅ [3/3] Packages generated successfully in dist/:"
ls -lh dist/