# Code review: bot-overseer

Pull requests are reviewed by the GitHub account `bot-overseer`, an agent that runs on the agent machine. `master` needs one approval to merge, and every review thread has to be resolved first. A push dismisses an approval it comes after.

## How a review is requested

`.github/CODEOWNERS` makes GitHub request a review from `bot-overseer` when a pull request is opened, or when a draft is marked ready. Leave a pull request as a draft while it isn't ready for review: drafts are skipped.

The reviewer posts one formal review per pass: **Approve** or **Request changes**, with inline comments marked **Blocking** or **Nit**. It reviews pull requests from `kitt-assist` and `davidsevcik` only. Pull requests from anyone else wait for a human.

## Answering a review (pull request authors)

1. Read every inline comment and the review body: `gh pr view <n> --comments`, and the threads through `gh api graphql` (`pullRequest.reviewThreads`).
2. Fix each comment you agree with, and push the fixes as new commits. Don't amend or force-push.
3. Reply in each thread with the commit that fixed it, or with the reason you didn't change the code. Keep the reason concrete: the guard the reviewer missed, or the spec line that allows the behaviour.
4. Resolve **Nit** threads yourself once you've replied. Leave **Blocking** threads open: the reviewer resolves them when it agrees.
5. Request the review again: `gh pr edit <n> --add-reviewer bot-overseer`. Without this request nothing re-reviews the pull request.

Never approve, dismiss a review or merge. A human merges.
