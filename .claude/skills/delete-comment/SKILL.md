---
name: delete-comment
description: Delete code comments, moving decisions into ADRs and shortcuts into issues, and lower the comment budget.
disable-model-invocation: true
argument-hint: "[path ...]  (default: every file that still has comments)"
---

# Delete comments

Code comments are banned in this repo ([ADR 0004](../../../docs/adr/0004-code-carries-no-comments.md)), except one package doc per package, bare `// ADR-NNNN` pointers and tool directives; `commentkit` never lists those. `internal/archtest/comments_test.go` holds every Go file to its budget in `internal/archtest/testdata/comment_budget.txt` (`<lines> <path>`), a ratchet that only goes down. This skill pays the budget down: every comment in scope gets a verdict, and its content is deleted, moved into an ADR or filed as an issue.

Scratch files go in `.scratch/delete-comment/` (gitignored). `commentkit` is `go run ./.claude/skills/delete-comment/commentkit`.

## 1. Pick the scope

Use the paths given as arguments. Without arguments, `commentkit list` finds every comment the budget test counts; if there are over about 300, take whole files from the top of `sort -rn internal/archtest/testdata/comment_budget.txt` (or, for a branch that predates the ban, the files it changes) and leave the rest for the next run. An empty ledger means there is nothing to do. Branch off `master` first.

Done when `commentkit list <paths> > .scratch/delete-comment/ledger.jsonl` has written the ledger.

## 2. Classify

Read [CLASSIFY.md](CLASSIFY.md) and write `.scratch/delete-comment/verdicts.jsonl`. Over about 300 comments, split the ledger with `commentkit list -chunks N -out .scratch/delete-comment/ledger <paths>` and hand each chunk to a `general-purpose` subagent with CLASSIFY.md as its brief and its own verdicts file to write. Subagents never edit the repo.

Done when every ledger id has exactly one verdict (check with a one-line `python3` or `jq` count), and you have spot-read a sample of each subagent's decisions and excuses against the code and fixed the wrong ones.

## 3. Move decisions into ADRs

Group the `decision` verdicts by area (package or subsystem). Each area gets one ADR in `docs/adr/` in the existing format (`# N. Title`, Date, Status, Context, Decision, Consequences), with one bullet per decision naming its symbol. If an ADR for that area exists, add the bullets to it rather than writing a second one. Where a decision contradicts an existing ADR, say so in the PR.

Then mark the traps. A decision's site gets a pointer, a line that is exactly `// ADR-NNNN` directly above the declaration or statement, only when a plausible "fix" there would break the decision **and** no existing test fails when it does. Find and read the test before deciding; where one guards the decision, the test is the guard. Several decisions in one function share one pointer. Expect pointers at a minority of sites.

Done when every `decision` verdict's content is in an ADR, each decision has a pointer or a named guarding test (or is judged no trap), and `go test ./internal/archtest -run ADRPointer` passes.

## 4. File the excuses

Group `excuse` verdicts that share a root cause. Check `gh issue list --state open` for duplicates, then file one issue per root cause with the `needs-triage` label: what the shortcut is (symbol, quoting the deleted comment), why it is a problem, and the `fix`.

Done when every `excuse` verdict is covered by an issue, new or existing.

## 5. Strip and lower the budget

```sh
go run ./.claude/skills/delete-comment/commentkit strip .scratch/delete-comment/verdicts*.jsonl
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 fmt ./...
go test ./internal/archtest -run TestCommentBudget -update-comments
```

`strip` removes every group not marked `keep` and leaves directives in place. It refuses to run on a stale ledger: re-list and re-classify if the code moved.

Done when `scripts/quality.sh` ends `OK`. If a golden screen changed, the deletion touched behaviour: find the cause; never regenerate the goldens to hide it.

## 6. Open the PR

Include in the body: counts per category, the lines shed (`git diff --shortstat`), the ADRs written and the issues filed. Comments stay banned: the budget file only ever loses lines.
