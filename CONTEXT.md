# bqt

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents.

## Language

**Favorite**:
A query the user chose to keep, stored as a plain `.sql` file with a small header for tags and description.
_Avoid_: Saved query, bookmark, snippet

**History entry**:
An automatic record of one query run: its SQL, project, bytes billed, duration and status. A History entry can be promoted to a Favorite.
_Avoid_: Past query, log line

**Billing project**:
The GCP project that query jobs are billed to, set once in the config file and rarely changed. It can differ from the project whose data is being read.
_Avoid_: Default project, quota project
