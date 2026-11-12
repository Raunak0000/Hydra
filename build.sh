#!/usr/bin/env bash
set -euo pipefail

export PATH="$PATH:$HOME/go/bin"

echo "🔨 [1/4] Compiling Go production binaries (-s -w)..."
mkdir -p bin dist
rm -f dist/*.deb dist/*.rpm dist/hydra-extension.zip dist/SHA256SUMS
go build -ldflags="-s -w" -o bin/hydra-daemon main.go
go build -ldflags="-s -w" -o bin/hydra cmd/hydra-cli/main.go
go build -ldflags="-s -w" -o bin/hydra-tui ./cmd/hydra-tui

echo "🧩 [2/4] Packaging Firefox WebExtension archive..."
if command -v zip >/dev/null 2>&1; then
    (cd extension && zip -rq ../dist/hydra-extension.zip . -x "package.json" "test_extension.mjs" ".*")
else
    echo "⚠️ zip not installed; skipping dist/hydra-extension.zip"
fi

echo "📦 [3/4] Building .deb and .rpm packages with nfpm..."
nfpm package --config nfpm.yaml --packager deb --target dist/
nfpm package --config nfpm.yaml --packager rpm --target dist/

echo "🔐 [4/4] Generating release checksums..."
(
    cd dist
    sha256sum hydra*.deb hydra*.rpm $( [ -f hydra-extension.zip ] && echo hydra-extension.zip ) > SHA256SUMS
)

echo "✅ Release artifacts generated successfully in dist/:"
ls -lh dist/
