# 3. Code carries no comments

Date: 2026-11-25
Status: accepted

## Context

The Go code had 3,042 comment groups (4,737 comment lines) in 197 files. Read one by one, 80% repeated what the code already said, 22 described code or behaviour that had since changed, and 17 excused a shortcut instead of fixing it. That last kind spreads: an agent reads the excuse as permission and copies the shortcut. The rest, 555 comments, recorded real decisions. They were scattered across files and were invisible to anyone not reading that exact line.

## Decision

- Go code has no comments. The only exceptions are lines that tools read: `//go:` directives, `//nolint:<linter> // reason` (the reason is required by `CONTRIBUTING.md`), `//line`, `//export`, `// Code generated`, example `// Output:` lines, and Go files under `testdata/`, which are test input.
- Intent goes into names and tests. A lasting decision goes into an ADR: the decisions the comments held are in ADRs 0004 to 0011, one per area. A known shortcut goes into an issue.
- `internal/archtest/comments_test.go` enforces the rule against a per-file budget in `internal/archtest/testdata/comment_budget.txt`, as a ratchet: a file over its budget fails, a file under it fails until the lower budget is written with `-update-comments`, and that flag never raises a budget. The budget starts empty, so today it works as a plain ban.
- `/delete-comment` (`.claude/skills/delete-comment/`) repeats the process: classify every comment, move decisions into ADRs, file excuses as issues, strip the rest with `commentkit`, and lower the budget.

## Consequences

- Go doc comments are gone, so `go doc` and the architecture map (`internal/tools/archmap`) show signatures without summaries.
- A non-obvious rule is now found in an ADR, not next to the code. ADRs name the symbol each decision applies to; renaming a symbol means updating its ADR.
- A comment can come back only by raising a budget by hand, and the diff to `comment_budget.txt` shows it in review.
- Shell scripts, YAML and other non-Go files keep their comments (81 lines). The rule does not cover them yet.
