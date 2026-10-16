# pr-body-staleness

English | [日本語](pr-body-staleness.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

After a successful `git push`, this hook compares the PR body's last edit time with commit dates. If it finds newer
commits, it asks the agent to check whether the body still describes the changes. A newer commit does not automatically
mean the body is wrong.

The default guidance refers to the `update-pr` skill. To replace it, set `hooks.pr-body-staleness.update-instruction` in `~/.config/hhx/config.yaml`:

```yaml
hooks:
  pr-body-staleness:
    update-instruction: "If the body is stale, update it with gh pr edit."
```

Right after a push, GitHub may still return the PR's commit list from before the push. The hook queries GitHub again
every 2 seconds until the PR head matches the pushed `HEAD`, then checks that list. It waits at most 20 seconds in total.
If the head still does not match after 20 seconds, it checks the last list it obtained and adds a note that the list may
be missing the commits just pushed. It adds this note even when that list has no commits newer than the body, because an
outdated list cannot show that there are none. It does not wait when the pushed `HEAD` has no open PR.

The hook calls authenticated `gh` and gives up silently if it cannot obtain the PR, including when a retry fails. Rebases and cherry-picks can change
commit dates, so it may ask for a check when the body is still correct. When you push a branch other than the current `HEAD`, or when
`HEAD` also belongs to another open PR (for example, stacked branches) that GitHub returns first, the head never matches,
so the hook waits the full 20 seconds and reports the list as possibly outdated.
