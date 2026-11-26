# Code review: bot-overseer

Pull requests are reviewed by the GitHub account `bot-overseer`. The reviewer runs outside this repo, so a pull request can't change its own reviewer. `master` needs one approval to merge, and every review thread has to be resolved first. A push dismisses an approval it comes after.

## How a review is requested

`.github/CODEOWNERS` makes GitHub request a review from `bot-overseer` when a pull request is opened, or when a draft is marked ready. Leave a pull request as a draft while it isn't ready for review: drafts are skipped.

The reviewer looks for requests every 5 minutes. To start it at once after a request, run `~/overseer/nudge` if that file exists on your machine.

The reviewer posts one formal review per pass: **Approve** or **Request changes**, with inline comments marked **Blocking** or **Nit**. It reviews pull requests from `kitt-assist` and `davidsevcik` only. Pull requests from anyone else wait for a human.

## Merging

When the reviewer approves a pull request from `kitt-assist`, it turns on auto-merge. GitHub then squash-merges the pull request by itself once every thread is resolved and CI passes. On an approved pull request, resolving the last **Nit** thread is what merges it. Other pull requests are merged by hand.

The reviewer leaves auto-merge off while another open pull request is based on the branch: the branch is deleted after the merge, and that would close the other pull request. Retarget it to `master` first, then request the review again.

## Answering a review (pull request authors)

Answer a review as soon as it lands, without waiting to be asked: fix what it reports and push. Stop and ask the user only when a finding needs their decision:

- The fixes differ in what users see or what the product does: scope, behaviour, wording, defaults.
- The fix could cost money, delete or migrate user data, or change credentials, CI, the ruleset or the reviewer.
- You disagree, and the reviewer kept the finding after your reply.
- The fix needs access you don't have, such as a GCP project or a secret.

For those, reply in the thread with the question and the options, leave it open, and tell the user. Fix everything else in the same pass.

1. Read every inline comment and the review body: `gh pr view <n> --comments`, and the threads through `gh api graphql` (`pullRequest.reviewThreads`).
2. Fix each comment you agree with, and push the fixes as new commits. Don't amend or force-push.
3. Reply in each thread with the commit that fixed it, or with the reason you didn't change the code. Keep the reason concrete: the guard the reviewer missed, or the spec line that allows the behaviour.
4. Resolve **Nit** threads yourself once you've replied. Leave **Blocking** threads open: the reviewer resolves them when it agrees, and reopens one that anyone else resolves.
5. Request the review again: `gh pr edit <n> --add-reviewer bot-overseer`, then nudge the reviewer as above. Without this request nothing re-reviews the pull request.

Never approve, dismiss a review, merge, or turn auto-merge on or off yourself.
