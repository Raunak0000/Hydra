#!/usr/bin/env bash
set -e

echo "🔨 [1/4] Generating Templ UI templates..."
templ generate

echo "🔨 [2/4] Compiling Go production binaries (-s -w)..."
mkdir -p bin dist
go build -ldflags="-s -w" -o bin/hydra-daemon main.go
go build -ldflags="-s -w" -o bin/hydra cmd/hydra-cli/main.go

echo "📦 [3/4] Building .deb and .rpm packages with nfpm..."
nfpm package --config nfpm.yaml --packager deb --target dist/
nfpm package --config nfpm.yaml --packager rpm --target dist/

echo "✅ [4/4] Packages generated successfully in dist/:"
ls -lh dist/