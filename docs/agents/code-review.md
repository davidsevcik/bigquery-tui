# Code review: bot-overseer

Pull requests are reviewed by the GitHub account `bot-overseer`. The reviewer runs outside this repo, so a pull request can't change its own reviewer. `master` needs one approval to merge, and every review thread has to be resolved first. A push dismisses an approval it comes after.

## How a review is requested

`.github/CODEOWNERS` makes GitHub request a review from `bot-overseer` when a pull request is opened, or when a draft is marked ready. Leave a pull request as a draft while it isn't ready for review: drafts are skipped.

Before any review request, bring the branch up to date if it is behind its base branch (`master`, or the branch of the pull request it is stacked on): merge the base branch in (don't rebase or force-push), resolve any conflicts, run `scripts/quality.sh` (see `CONTRIBUTING.md`) and push. The review is then of the code as it would merge.

The reviewer looks for requests every 5 minutes. To start it at once after a request, run `~/overseer/nudge` if that file exists on your machine.

The reviewer posts one formal review per pass: **Approve** or **Request changes**, with inline comments marked **Blocking** or **Nit**. It reviews pull requests from `kitt-assist` and `davidsevcik` only. Pull requests from anyone else wait for a human.

## Merging

When the reviewer approves a pull request from `kitt-assist`, it turns on auto-merge. GitHub then squash-merges the pull request by itself once every thread is resolved and CI passes. On an approved pull request, resolving the last **Nit** thread is what merges it. Other pull requests are merged by hand.

If an approved pull request falls behind its base before it merges, the reviewer merges the base in itself (GitHub's update-branch). The approval stays, so the pull request merges once CI passes, and the author has nothing to do. If that merge conflicts, the reviewer says so in a comment: merge the base in yourself, resolve the conflicts and request the review again.

The reviewer leaves auto-merge off while another open pull request is based on the branch: the branch is deleted after the merge, and that would close the other pull request. Retarget it to `master` first, then request the review again.

## Answering a review (pull request authors)

Answer a review as soon as it lands, without waiting to be asked, by following the steps below. Ask the user only about a finding that needs their decision:

- The fix, or the choice between possible fixes, changes what users see or what the product does: scope, behaviour, wording, defaults.
- The fix could cost money, delete or migrate user data, or change credentials, CI, the `master` ruleset, `.github/CODEOWNERS` or how reviews are requested.
- You disagree, and the reviewer kept the finding after your reply.
- The fix needs access you don't have, such as a GCP project or a secret.

For each of those, reply in its thread with the question and the options, tell the user, and go on with every other finding. Leave that thread open, even a **Nit**: on an approved pull request, resolving it would merge the pull request before the user answers.

1. Read every inline comment and the review body: `gh pr view <n> --comments`, and the threads through `gh api graphql` (`pullRequest.reviewThreads`).
2. Fix each comment you agree with, and push the fixes as new commits. Don't amend or force-push.
3. Reply in each thread with the commit that fixed it, or with the reason you didn't change the code. Keep the reason concrete: the guard the reviewer missed, or the spec line that allows the behaviour.
4. Resolve **Nit** threads yourself once you've replied, except one waiting on the user's decision. Leave **Blocking** threads open: the reviewer resolves them when it agrees, and reopens one that anyone else resolves.
5. Once you've pushed a fix or replied with a reason, bring the branch up to date as above, then request the review again: `gh pr edit <n> --add-reviewer bot-overseer`, then nudge the reviewer as above. Without this request nothing re-reviews the pull request. Don't request it when the only change since the review is a question waiting on the user: request it after the user answers and you've acted on it.

Never approve, dismiss a review, merge, or turn auto-merge on or off yourself.
