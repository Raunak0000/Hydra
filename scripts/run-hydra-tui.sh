#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ ! -f .env.local ]]; then
    echo "Missing .env.local"
    exit 1
fi

set -a
source .env.local
set +a

if [[ -z "${HYDRA_API_TOKEN:-}" ]]; then
    echo "HYDRA_API_TOKEN is missing from .env.local"
    exit 1
fi

exec go run ./cmd/hydra-tui "$@"
