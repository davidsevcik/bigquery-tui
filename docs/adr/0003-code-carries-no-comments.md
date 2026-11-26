# 3. Code carries no comments, beyond package docs and ADR pointers

Date: 2026-11-25
Status: accepted

## Context

The Go code had 3,042 comment groups (4,737 comment lines) in 197 files. Read one by one, 80% repeated what the code already said, 22 described code or behaviour that had since changed, and 17 excused a shortcut instead of fixing it. That last kind spreads: an agent reads the excuse as permission and copies the shortcut. The rest, 555 comments, recorded real decisions. They were scattered across files and were invisible to anyone not reading that exact line.

Where a comment sat predicted its value. 82% of the doc comments on functions and types were tautologies, but two thirds of the 27 package docs recorded a decision, and a package doc is where a reader finds their bearings. Moving every decision into ADRs also loses its locality: someone editing a function does not open an ADR unless something at the site sends them there.

## Decision

- Go code has no comments, with three exceptions:
  - **Package docs**, one per package: what the package is for and where its boundary lies.
  - **ADR pointers**: a line that is exactly `// ADR-NNNN`, on or inside the declaration a decision is about. It carries no prose. A pointer marks a trap: a site where a plausible "fix" breaks a decision and no test would catch it. Where a test guards the decision, the test is the guard and there is no pointer.
  - **Lines that tools read**: `//go:` directives, `//nolint:<linter> // reason` (the reason is required by `CONTRIBUTING.md`), `//line`, `//export`, `// Code generated`, example `// Output:` lines, and Go files under `testdata/`, which are test input.
- Intent goes into names and tests. A lasting decision goes into an ADR: the decisions the comments held are in ADRs 0004 to 0011, one per area. A known shortcut goes into an issue.
- `internal/archtest/comments_test.go` enforces it. Every other comment counts against a per-file budget in `internal/archtest/testdata/comment_budget.txt`, a ratchet: a file over its budget fails, a file under it fails until the lower budget is written with `-update-comments`, and that flag never raises a budget. The budget starts empty. The same tests allow one package doc per package, and check that each pointer's ADR exists and mentions the declaration the pointer sits on.
- `/delete-comment` (`.claude/skills/delete-comment/`) repeats the process: classify every comment, move decisions into ADRs with pointers where they are traps, file excuses as issues, strip the rest with `commentkit`, and lower the budget.

## Consequences

- `go doc` and the architecture map (`internal/tools/archmap`) show package summaries, but signatures without per-symbol docs.
- A non-obvious rule lives in an ADR. The ADRs name the symbol each decision applies to, so renaming a symbol means updating its ADR; for a symbol with a pointer, the pointer test fails until you do.
- A comment can come back only by raising a budget by hand, and the diff to `comment_budget.txt` shows it in review.
- Shell scripts, YAML and other non-Go files keep their comments (81 lines). The rule does not cover them yet.
