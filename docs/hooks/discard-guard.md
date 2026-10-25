# discard-guard

English | [日本語](discard-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

Before a Git command discards uncommitted changes, this hook saves tracked and untracked files in the reflog of
`refs/hhx/discard-snapshot`. It covers commands such as `git reset --hard`, forced checkout or switch, `git restore`,
and `git clean -f`. A successful snapshot lets the command proceed; an unresolved target or failed snapshot blocks it.
The working tree and index are not changed by the snapshot.

## How the target is found

The hook follows `cd` and `git -C` through the command to find the repository each discarding command runs in.
Paths can be literals, start with `~/`, or use variables assigned earlier in the same command, such as
`S=/abs/path; D=$S/clone; git -C $D reset --hard`. A variable counts only when its name appears once in the command,
in a plain assignment that runs unconditionally. Assignments inside `if`, `for`, `{ ... }`, or a subshell, after `&&`
or `||`, before a command (`D=x git ...`), or from `$(...)` are not followed. Neither are environment variables.
A target the hook cannot follow blocks the command.

## Disposable clones in the scratchpad

In Claude Code, discarding inside the session's scratchpad (`/tmp/claude-<uid>/<project>/<session_id>/scratchpad`)
proceeds without a snapshot. The agent creates disposable clones there for verification. The target may not exist yet
when the hook runs, as when the same command clones it first. In that case, a `cd` into it must be joined to the
discarding command with `&&`, or run on its own line under `set -e`, so that a failed `cd` cannot leave the command in
another repository. Symlinks are resolved first, and a target whose work tree is outside the scratchpad gets a
snapshot. A command that runs `ln` does not get this exemption for paths that do not exist yet. The check matches the
path layout of Claude Code's scratchpad and is turned off for Codex sessions.

A here-document written to a file (`cat > x.sh <<'EOF' ... EOF`, `tee x.sh <<EOF`) is not checked, because its body
does not run in that command. If the file name appears again in the command, as when the script runs right away, or if
an unquoted body contains `$(...)` or backquotes, the body is still checked. A here-document fed to a shell
(`bash <<'EOF'`) is always checked.

## Restore a snapshot

Find the entry marked with your worktree's path, then substitute its number for `N`:

```sh
git reflog show --format='%gd %gs' refs/hhx/discard-snapshot
git restore --source='refs/hhx/discard-snapshot@{N}' --worktree -- .
```

This restores the working tree, not the index. Ignored files and uncommitted changes inside submodules are not saved. Snapshots expire with the reflog.
