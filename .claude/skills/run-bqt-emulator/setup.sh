#!/bin/bash
# Starts the BigQuery emulator with seed data and prepares an isolated bqt:
# binary, config, data dir and a synced catalog under .scratch/emu.
# Re-running resets everything. Usage: .claude/skills/run-bqt-emulator/setup.sh
set -euo pipefail
SKILL=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$SKILL/../../.." && pwd)
E="$REPO/.scratch/emu"   # under $HOME: colima shares only the home directory
API=http://localhost:9050/bigquery/v2/projects/test-proj

colima status >/dev/null 2>&1 || colima start
rm -rf "$E" && mkdir -p "$E/data" "$E/fav" "$E/exports"
python3 "$SKILL/seed.py" > "$E/data.yaml"

docker rm -f bqemu >/dev/null 2>&1 || true
docker run -d --name bqemu -p 9050:9050 -v "$E:/work" \
  ghcr.io/goccy/bigquery-emulator:latest --project=test-proj --data-from-yaml=/work/data.yaml >/dev/null
for _ in $(seq 1 60); do curl -sf "$API/datasets" >/dev/null && break; sleep 0.5; done
curl -sf "$API/datasets" >/dev/null || { echo "emulator did not start"; docker logs bqemu | tail; exit 1; }

# what the YAML cannot express: a view, and a table that requires a partition filter
curl -sf -X POST "$API/queries" -H 'content-type: application/json' \
  -d '{"query":"CREATE VIEW sales.v_big_orders AS SELECT id, amount FROM sales.orders WHERE amount > 500","useLegacySql":false}' >/dev/null
curl -sf -X POST "$API/datasets/sales/tables" -H 'content-type: application/json' \
  -d '{"tableReference":{"projectId":"test-proj","datasetId":"sales","tableId":"orders_part"},"schema":{"fields":[{"name":"id","type":"INT64","mode":"REQUIRED"},{"name":"day","type":"DATE"},{"name":"amount","type":"FLOAT64"}]},"timePartitioning":{"type":"DAY","field":"day"},"requirePartitionFilter":true}' >/dev/null
curl -sf -X POST "$API/datasets/sales/tables/orders_part/insertAll" -H 'content-type: application/json' \
  -d '{"rows":[{"json":{"id":1,"day":"2024-01-01","amount":1.5}},{"json":{"id":2,"day":"2024-01-02","amount":2.5}}]}' >/dev/null

cat > "$E/config.toml" <<TOML
data_dir = "$E/data"
favorites_dir = "$E/fav"
export_dir = "$E/exports"
TOML
cd "$REPO"
go build -o "$E/bqt" ./cmd/bqt
go build -o "$E/emuseed" ./.claude/skills/run-bqt-emulator/emuseed
export BQT_CONFIG="$E/config.toml" BQT_BIGQUERY_ENDPOINT=http://localhost:9050
"$E/bqt" projects select test-proj >/dev/null 2>&1
"$E/emuseed"
echo "ready: $E (bqt, config.toml, data/)"
