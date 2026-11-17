#!/usr/bin/env bash
set -euo pipefail
exec "${FACTORY_BIN:-factory}" workflow watch "$@"
