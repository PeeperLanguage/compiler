#!/bin/bash
set -eo pipefail

echo "Executing..."
go run ./scripts/bundle.go
echo "Done."