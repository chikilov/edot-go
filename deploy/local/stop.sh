#!/bin/sh
set -eu
local_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# Preserve all data volumes; this does not remove the cluster's data.
docker compose --project-directory "$local_dir" stop
