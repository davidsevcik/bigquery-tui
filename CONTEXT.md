# bqt

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents.

## Language

**Favorite**:
A query the user chose to keep, stored as a plain `.sql` file with a small header for tags and description.
_Avoid_: Saved query, bookmark, snippet

**History entry**:
An automatic record of one query run: its SQL, project, bytes billed, duration and status. A History entry can be promoted to a Favorite.
_Avoid_: Past query, log line

**Run**:
One execution of a SQL snapshot, with its own state, dry-run estimate and result pager. Several Runs can be in flight at once. A finished Run produces a History entry.
_Avoid_: Job (that is the BigQuery object behind a Run), query tab

**Execution project**:
The GCP project that query jobs run in, set once in the config file and rarely changed. Under on-demand pricing it is billed for the bytes processed; under capacity pricing it is assigned to a reservation, and slots are billed to the reservation's administration project. It can differ from the project whose data is being read.
_Avoid_: Billing project, default project, quota project
