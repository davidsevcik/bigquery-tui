#!/bin/bash
# Prepares the screenshot demo: a BigQuery emulator with the fictional acme data
# (seed.py), a Linux bqt, and a catalog synced from the emulator, all under
# .scratch/shots. Re-running resets everything. record.sh runs it first.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../.." && pwd)
E="$REPO/.scratch/shots"   # under $HOME: colima shares only the home directory
NET=bqt-shots

docker info >/dev/null 2>&1 || colima start
rm -rf "$E" && mkdir -p "$E/data" "$E/fav" "$E/out"
python3 "$HERE/seed.py" > "$E/data.yaml"

docker network inspect $NET >/dev/null 2>&1 || docker network create $NET >/dev/null
docker rm -f bqt-shots-emu >/dev/null 2>&1 || true
docker run -d --name bqt-shots-emu --network $NET -p 9051:9050 -v "$E:/work" \
  ghcr.io/goccy/bigquery-emulator:latest --project=acme-prod --data-from-yaml=/work/data.yaml >/dev/null
API=http://localhost:9051/bigquery/v2/projects/acme-prod
for _ in $(seq 1 120); do curl -sf "$API/datasets" >/dev/null && break; sleep 0.5; done
curl -sf "$API/datasets" >/dev/null || { echo "emulator did not start"; docker logs bqt-shots-emu | tail; exit 1; }

# a view, which the YAML cannot express
curl -sf -X POST "$API/queries" -H 'content-type: application/json' -d '{"useLegacySql":false,"query":
  "CREATE VIEW sales.v_daily_revenue AS SELECT DATE(created_at) AS day, country, SUM(amount) AS revenue FROM sales.orders WHERE status IN (\"delivered\", \"shipped\") GROUP BY 1, 2"}' >/dev/null

# one config for seeding from this machine, one for bqt inside the VHS container
cat > "$E/config.toml" <<TOML
data_dir = "$E/data"
favorites_dir = "$E/fav"
TOML
cat > "$E/config.vhs.toml" <<TOML
data_dir = "/work/data"
favorites_dir = "/work/fav"
TOML

cd "$REPO"
GOOS=linux GOARCH=$(docker info -f '{{.Architecture}}' | sed 's/aarch64/arm64/;s/x86_64/amd64/') \
  go build -o "$E/bqt" ./cmd/bqt
go build -o "$E/bqt-host" ./cmd/bqt
go build -o "$E/emuseed" ./.claude/skills/run-bqt-emulator/emuseed
export BQT_CONFIG="$E/config.toml" BQT_BIGQUERY_ENDPOINT=http://localhost:9051
"$E/bqt-host" projects select acme-prod acme-staging >/dev/null 2>&1
"$E/bqt-host" projects exec acme-prod >/dev/null 2>&1
"$E/emuseed"
echo "ready: $E"
