# Classifying comments

The brief for whoever classifies a ledger: the main agent, or a subagent handed one chunk. Classify only; `commentkit strip` does the deletions. Domain terms are in `CONTEXT.md`, existing decisions in `docs/adr/`.

## Input

A ledger file from `commentkit list`: one JSON object per comment group, `{"id","file","line","end","own_line","text"}`. `id` is `file:line`. `own_line` is false for a trailing comment after code. Directives (`//go:build`, `//nolint:x // reason`) are never in the ledger; a group with an embedded `//nolint` lists only its prose, and strip keeps the directive.

## Output

A verdicts file with **exactly one line per ledger id**:

```json
{"id":"internal/store/catalog.go:120","cat":"tautology|stale|excuse|decision|keep","why":"<=15 words","symbol":"store.(*Catalog).Apply","decision":"...","hack":"...","fix":"..."}
```

- `symbol`: the Go symbol the comment sits on or in. Required for `decision` and `excuse`.
- `decision` (`cat: decision` only): 1-3 standalone sentences, readable without the code: what was chosen, over what alternative if known, and why. Name the symbol, never a line number. Keep the original's facts and invent no rationale.
- `hack` and `fix` (`cat: excuse` only): the shortcut, and what a proper implementation does instead.

## Categories

Read the code around every comment, whole files where needed. Then pick one:

- **tautology**: repeats what the code already shows: the name, the signature, the next lines, a section label ("Open opens the catalog"), a test comment restating the test name or its assertion.
- **stale**: names code, behaviour, files, spec sections or plans that no longer exist or no longer hold. Grep every identifier it names before claiming this. A done TODO is stale.
- **excuse**: defends a shortcut in our own code that should be fixed instead: "for now", "simplest", "good enough", "workaround until", duplicated logic with a justification, a swallowed error with a rationalization, a sleep or poll where an event would do, a known limitation the code lives with. A workaround for an external system that *is* the right design is a decision.
- **decision**: an important, non-obvious choice the code relies on: a trade-off, an invariant, an ordering requirement, why the obvious alternative is wrong, how BigQuery, SQLite, the terminal or the OS behaves and shapes the code, a security or concurrency reason. The test: with the comment gone, would a competent maintainer plausibly "fix" the code into a bug or re-argue a settled choice? Yes is a decision; "they would read the code and understand" is a tautology.
- **keep**: a tool or test reads the comment text. Name the reader in `why`. Expect almost none.

A group with mixed content takes its most valuable category (decision > excuse > stale > tautology), and `decision` keeps only the decision part.

Grade in both directions: a well-written comment that only explains *what* is a tautology; a terse one that prevents a real bug is a decision.

Done when every ledger id has exactly one parsing line in the verdicts file.
