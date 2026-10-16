#!/usr/bin/env bash
# Builds the compiler from source into build/: first writes the generated code
# again (go generate), so a build never uses stale generated files, then builds
# build/bin/peeper and copies the bundled libraries to build/libs, where the
# compiler looks for them (next to its bin folder).
set -euo pipefail
cd "$(dirname "$0")/.."

go generate ./...
go build -o build/bin/peeper ./cmd
rm -rf build/libs
mkdir -p build/libs
cp -R _builtin_library/. build/libs/

echo "built build/bin/peeper with libraries in build/libs"
