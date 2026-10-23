package config

// Template returns the commented config written by `bqt init`.
func Template() string { return template }

const template = `# bqt configuration. Unknown keys are errors, so typos do not go unnoticed.
# Precedence: command-line flag > BQT_<KEY> environment variable > this file.
#
# Your projects are not set here: pick the ones to sync, and the Execution
# project that query and sync jobs run in, in the TUI's Projects dialog
# (ctrl+x p) or with ` + "`bqt projects`" + `.

# Default job location for your own queries. Leave unset to let BigQuery infer
# it from the datasets a query references. Sync never uses this: dataset
# locations are discovered automatically.
# location = "EU"

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

# Where exported query results are saved. Leave unset to default to the
# working directory. Created if missing.
# export_dir = "~/bqt-exports"

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
# editing = "vim"   # the query editor: "vim" (modes, motions, operators) or "plain" (modeless)
`
