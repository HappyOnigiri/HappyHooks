# blocking-command-guard

English | [日本語](blocking-command-guard.ja.md) · [Hook list](../../README.md#hooks)

**Default:** Enabled for Claude Code and Codex.

Rejects recognizable input waits, interactive operations, and continuous foreground modes in PreToolUse.
It does not monitor slow or silent processes and does not add timeouts.

## Targets

| Category | Examples |
|---|---|
| Reading without an input source | `cat`, `tee OUTPUT`, `sort`, `wc -l`, `head`, `grep needle`, `sed -n p`, `read -r line` |
| Reading a script without an input source | `python3 -`, `python3`, `sh -s`, and bare `node`, `ruby`, or `perl` |
| Explicit interactive modes | `git add -p`, `git rebase -i HEAD~2`, `git commit --edit`, `python3 -i`, `node -i` |
| Interactive commands | `vi`, `vim`, `nvim`, `nano`, `emacs`, `top`, `htop`, and `fzf` without options |
| Continuous foreground modes | `tail -f FILE`, `tail -F FILE`, `watch date`, `yes` |

Arguments, input sources, and execution modes can exempt these commands.
Recognizes standard command names and their `/bin/` and `/usr/bin/` paths; arbitrary scripts with the same basename are excluded.

## Input and background execution

Uses a shell syntax tree to distinguish pipes, heredocs, input redirections, blocks, and subshells.
These commands have explicit input sources and pass:

```sh
cat < input.txt
printf data | cat
python3 - <<'PY'
print('ok')
PY
```

Also tracks persistent input redirections such as `exec < input.txt; cat`.
Output redirections such as `> OUTPUT` and `2>/dev/null` do not provide input.
An input wait in the middle of a command list, such as `cat > OUTPUT; python3 ...`, rejects the entire tool call before any part executes.
This also prevents output truncation and delayed execution of the remaining commands.

Explicit `&` and Bash tool input `run_in_background: true` pass.
Continuous modes piped to a downstream consumer, such as `tail -f FILE | head -n 1` and `yes | head -n 1`, also pass.
These exemptions avoid speculative rejection; they do not guarantee termination.

## Conservative limits

Passes rather than guesses in these cases:

- Unparseable syntax, commands over 128 KiB, and calls containing variable expansion, command substitution, or globbing.
- Command lists containing function definitions, assignments, aliases, `source`, `eval`, `trap`, `exit`, or `return`.
- Command lists with shell configuration, declaration, or command-resolution builtins such as `set`, `export`, or `command`.
- Conditional and loop bodies, and subsequent input reads that may inherit changed input from them.
- Unknown options and finite or non-interactive forms such as `vim -es`, `nvim --headless`, `emacs --batch`, `top -l 1`, and `fzf --filter`.
- Pagers, authentication, and implicit editors that depend on TTYs or configuration. `less FILE`, `more FILE`, and ordinary `git log` pass.

Only limited `grep` and `sed` forms are analyzed. `awk` scripts, `sh -c` strings, and script file contents are not analyzed.
The guard does not predict whether an explicit input source is a blocking FIFO or terminal.
It also cannot guarantee whether startup errors, existing configuration, or external state will make a targeted mode exit early.

## Correcting a rejected command

The denial includes the entire command and a category: `STDIN`, `INTERACTIVE`, `EDITOR`, `FOLLOW`, or `STREAM`.
Specify input and required values, select a non-interactive finite mode, or remove an unnecessary command.
Explicitly background intentional continuous work.

The guidance does not recommend timeouts or automatically approving destructive actions to avoid waiting.
The guard does not monitor or kill running processes, redirect stdin to `/dev/null`, or rewrite commands.
Waits that cannot be determined from the command text are outside its scope.
