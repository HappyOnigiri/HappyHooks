# dangerous-rm-guard

English | [日本語](dangerous-rm-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code only.

Denies forms of `rm` and `rmdir` that Claude Code's built-in safety check would turn into a confirmation prompt.
Examples include a path starting with a possibly empty variable, a target that cannot be resolved statically, a critical
directory, or the current working directory and its ancestors.

The agent receives a reason it can act on instead of leaving the user waiting at a prompt.
The main rules mirror Claude Code v2.1.239. Deletion through a variable derived from an earlier assignment,
such as `D=$S/$n; rm -rf $D`, follows v2.1.283.

Scripts passed to a shell with `-c` (`bash -c '...'`) follow v2.1.296. The hook denies a script that Claude Code cannot
check statically (brace patterns such as sed's `\{0,1\}`, `alias`, `IFS`, or more than 2000 characters) when a word
in command position may turn into `rm` at run time, for example `$CMD x` or `direnv exec "$DIR"`.
This check needs no `rm` in the command. When the script can be checked, its contents go through the same rules,
with positional arguments replaced by their literal values. Setting `CLAUDE_CODE_DISABLE_INLINE_SHELL_RM_PROMPT`
turns off this check in both Claude Code and the hook. Claude Code can also turn the check off with a server-side
feature flag that the hook cannot see; in that case only the hook denies.

Expansions that Claude Code's shell parser cannot read follow v2.1.309. Claude Code asks for confirmation when its
parser cannot read a command, even without `rm` in it. The hook denies the two forms seen in practice:

- zsh-only syntax right after `${`, such as the parameter flags in `${(f)"$(cmd)"}` and `${(M)FILES:#*.rb}`, nested
  forms such as `${${x}}`, and subscript flags such as `${x[(r)pat]}`.
- Inside `$(...)`, a subshell or an array assignment whose closing `)` is followed right away by another `)`, such as
  `$(a || (b))`, `"$( (cd x && ls))"`, and `$(x=(a b))`. Claude Code's parser reads `))` as one token and cannot find
  where the `$(...)` ends. A space between the two `)` (`$(a || (b) )`) passes.

Inside single quotes, `$'...'`, comments, and quoted heredocs, these are not expansions and are left alone.
Other forms that Claude Code's parser cannot read are not covered.

Other checks may differ from newer versions.
