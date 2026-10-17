# push-ci-context

English | [日本語](push-ci-context.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

After a successful `git push` to a branch other than `main`, or `gh pr create`, this hook tells the agent to finish its remaining work.
The agent then runs `hhx wait-ci --progress` to wait for PR checks and report their result once. This avoids repeated short checks and prevents the
agent from ending its turn before CI finishes.
Direct pushes to `main` do not trigger this guidance. Running `hhx wait-ci` without a reference on `main` also returns immediately, reporting that there are no PR checks to watch.
The same applies to a detached HEAD that matches the local `origin/main` after a direct push.
On a detached HEAD, or when the current branch has no PR (for example after `git push origin HEAD:<other-branch>`), `hhx wait-ci` finds the PR from the HEAD commit.
If the current branch was pushed under its own name and `origin/<branch>` matches `HEAD`, it skips without searching, because the PR has not been created yet.

After a successful `gh workflow run`, it instead guides the agent to watch that workflow with `gh run watch`. Failed or
dry-run pushes do not trigger the guidance. The hook adds context; it does not make a permission decision.
