## Agent skills

### Issue tracker

Issues and specs are GitHub Issues on `davidsevcik/bigquery-tui`, managed with `gh`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

### Code comments

Go code carries no comments beyond package docs (ADR 0003, enforced by `internal/archtest`): put intent in names and tests, decisions in `docs/adr/`, shortcuts in issues. A `// ADR-NNNN` line marks a decision that is easy to break: read that ADR's entry for the symbol before changing the code there. To clear comments from a branch, the user runs `/delete-comment`.
