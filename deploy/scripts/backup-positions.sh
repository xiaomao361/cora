#!/usr/bin/env bash
set -euo pipefail

positions=${1:?usage: backup-positions.sh POSITIONS_FILE BACKUP_ROOT}
backup_root=${2:?usage: backup-positions.sh POSITIONS_FILE BACKUP_ROOT}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
destination="$backup_root/$timestamp/positions.json"

test -r "$positions"
mkdir -p "$(dirname "$destination")"
cp -p "$positions" "$destination"
chmod 0600 "$destination"
cmp "$positions" "$destination"
echo "$destination"
