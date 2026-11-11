#!/bin/sh
set -eu
local_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 "$local_dir/prepare.py"
docker compose --project-directory "$local_dir" up -d --force-recreate --wait --wait-timeout 300
