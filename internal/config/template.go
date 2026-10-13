package config

import "strings"

// Template returns the commented config written by `bqt init`. If
// executionProject is non-empty it is written uncommented.
func Template(executionProject string) string {
	t := template
	if executionProject != "" {
		t = strings.Replace(t, `# execution_project = "my-gcp-project"`, `execution_project = "`+executionProject+`"`, 1)
	}
	return t
}

const template = `# bqt configuration. Unknown keys are errors, so typos do not go unnoticed.
# Precedence: command-line flag > BQT_<KEY> environment variable > this file.

# The GCP project query and sync jobs run in (required). It needs the
# bigquery.jobs.create permission. Under on-demand pricing it is billed for the
# bytes processed; under a reservation, slots are billed to the reservation's
# administration project. bqt detects which applies from each job.
# execution_project = "my-gcp-project"

# Default job location for your own queries. Leave unset to let BigQuery infer
# it from the datasets a query references. Sync never uses this: dataset
# locations are discovered automatically.
# location = "EU"

# Data projects to keep in the local catalog (an explicit allowlist).
# projects = ["analytics-prod", "analytics-staging"]

# Gitignore-style patterns over project.dataset.table, applied at sync time.
# '*' stays inside one segment, '**' spans segments, '!' negates, the last
# matching line wins, and a shorter pattern covers everything beneath it.
# ignore = [
#   "sandbox-proj",          # a whole project
#   "analytics-prod.tmp_*",  # datasets
#   "analytics-prod.events.events_2*",
#   "!analytics-prod.tmp_keep",
# ]

# Where catalog.db, history.db, drafts and logs live.
# data_dir = "~/.local/share/bqt"

# Where Favorite .sql files live (can point into a git repository).
# favorites_dir = "~/.local/share/bqt/favorites"

# Refresh a project in the background when its last sync is older than this.
# sync_ttl = "24h"

# Ask for confirmation above this many scanned bytes. "auto" means 10GiB when
# the job runs on-demand and off when it runs on a reservation.
# confirm_bytes = "auto"

# Hard cap sent to BigQuery as MaximumBytesBilled on every job ("off" disables).
# max_bytes_billed = "1TiB"

# Limits for the JSON CLI and the MCP server. Flags and tool arguments can only
# lower these; raising them needs this file.
# agent_max_bytes = "1GiB"
# agent_timeout = "60s"
# cli_allow_writes = false

# Settings that only the terminal UI reads.
# [tui]
`
