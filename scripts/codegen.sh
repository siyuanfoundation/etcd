#!/usr/bin/env bash
# Generates some go files.

set -o errexit
set -o nounset
set -o pipefail

go run ./tools/codegen/main.go