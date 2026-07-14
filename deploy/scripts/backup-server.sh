#!/usr/bin/env bash
set -euo pipefail

server_binary=${1:?usage: backup-server.sh SERVER_BINARY DATABASE BACKUP_ROOT}
database=${2:?usage: backup-server.sh SERVER_BINARY DATABASE BACKUP_ROOT}
backup_root=${3:?usage: backup-server.sh SERVER_BINARY DATABASE BACKUP_ROOT}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
destination="$backup_root/cora-$timestamp.db"

mkdir -p "$(dirname "$destination")"
"$server_binary" -db "$database" -backup-db "$destination"
chmod 0600 "$destination"
"$server_binary" -db "$destination" -check-db
echo "$destination"
