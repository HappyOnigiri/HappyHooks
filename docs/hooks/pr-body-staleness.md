# pr-body-staleness

English | [日本語](pr-body-staleness.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

After a successful `git push`, this hook compares the PR body's last edit time with commit dates. If it finds newer
commits, it asks the agent to check whether the title and body still describe the changes, and shows the current title.
A newer commit does not automatically mean the body is wrong. Editing only the title does not reset the check, because
the hook compares commit dates with the body's last edit time.

The default guidance tells the agent to update the title and body in this turn using available tools; no particular skill is required.
It must complete the check and any necessary update before waiting for CI, and report why if no update is needed.
Unless a higher-priority instruction requires approval, it must not hand the update back to the user as a recommendation or follow-up task.
To replace it, set `hooks.pr-body-staleness.update-instruction` in `~/.config/hhx/config.yaml`:

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

The hook inspects the `HEAD` of the working directory the agent reports, not of the directory the command pushed from.
When no open PR is linked to that `HEAD`, GitHub responded normally, and the push command changed directories first
(`cd` or `pushd` in an earlier segment, or `git -C`), the hook adds a generic note instead of staying silent: the pushed
branch may belong to a PR it did not check, so check that PR's title and body against the commits just pushed. It does not
add this note when `gh` fails or when the command did not change directories.
