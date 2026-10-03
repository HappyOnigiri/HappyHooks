package githookspathguard

import (
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

// keyName はテストに書くキーの表記である。本体は大文字小文字を区別しない。
const keyName = "core.hooksPath"

// enabled はこの hook を明示的に有効にする設定である。
const enabled = "hooks:\n  git-hookspath-guard:\n    enabled: true\n"

func runRaw(t *testing.T, raw string) hooktest.Result {
	t.Helper()
	return hooktest.Run(t, Definition(), raw, hooktest.Options{Config: enabled})
}

func runArgv(t *testing.T, command string) hooktest.Result {
	t.Helper()
	return hooktest.Run(t, Definition(), "", hooktest.Options{Config: enabled, Args: []string{command}})
}

func check(t *testing.T, want string, commands ...string) {
	t.Helper()
	for _, command := range commands {
		got := runRaw(t, hooktest.BashPayload(command, "/tmp/repo", nil))
		if got.Decision != want {
			t.Errorf("command %q: decision=%q, want %q", command, got.Decision, want)
		}
	}
}

func fileToolPayload(toolName, filePath string) string {
	return hooktest.ToolPayload(toolName, map[string]any{"file_path": filePath, "content": "[core]\n\tbare = false\n"}, "/tmp/repo")
}

func TestEnabledByDefault(t *testing.T) {
	for _, raw := range []string{
		hooktest.BashPayload("git config "+keyName+" hooks", "/tmp/repo", nil),
		fileToolPayload("Write", "/tmp/repo/.git/config"),
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("default must be enabled: %+v", got)
		}
	}
	if got := hooktest.Argv(t, Definition(), "git config "+keyName+" hooks"); got.Decision != hooktest.Deny {
		t.Errorf("argv path must also be enabled by default: %+v", got)
	}
	explicit := hooktest.Run(t, Definition(), hooktest.BashPayload("git config "+keyName+" hooks", "/tmp/repo", nil),
		hooktest.Options{Config: "hooks:\n  git-hookspath-guard:\n    enabled: false\n"})
	if explicit.Decision != "" {
		t.Errorf("enabled: false must disable: %+v", explicit)
	}
}

func TestBlocksConfigWithValue(t *testing.T) {
	check(t, hooktest.Deny,
		"git config "+keyName+" .githooks",
		"git config --local "+keyName+" .githooks",
		"git config --global "+keyName+" ~/.config/git/hooks",
		"git config --system "+keyName+" /etc/githooks",
		"git config -f .git/config "+keyName+" hooks",
		"git config --file /tmp/cfg "+keyName+" hooks",
		"git config "+keyName+" `pwd`/hooks",
		"git config "+keyName+" $(pwd)/hooks",
		"git config "+keyName+` ""`,
		"git config "+keyName+" ''",
		"/usr/bin/git config "+keyName+" hooks",
		"git -C /other config "+keyName+" hooks",
		"cd repo && git config "+keyName+" ../hooks",
		"git config "+keyName+" hooks && npm test",
		"true; git config "+keyName+" hooks",
		"echo x | git config "+keyName+" hooks",
		"git config --get "+keyName+" || git config "+keyName+" .githooks",
		"git config "+keyName+" hooks\n",
		// Python の str.split() と同じく、Unicode の空白でもトークンを分ける。
		"git config "+keyName+"\u3000hooks",
	)
}

func TestBlocksKeyOperations(t *testing.T) {
	check(t, hooktest.Deny,
		"git config --unset "+keyName,
		"git config --unset-all "+keyName,
		"git config --add "+keyName+" hooks",
		"git config --replace-all "+keyName+" hooks",
		"git config --global --unset "+keyName,
	)
}

func TestBlocksOneshotOverride(t *testing.T) {
	check(t, hooktest.Deny,
		"git -c "+keyName+"=/dev/null commit -m x",
		"git -c "+keyName+"= commit -m x",
		"git -c "+keyName+"=.githooks push",
		"git -c user.name=x -c "+keyName+"=y commit -m z",
	)
}

// git の設定キーは大文字小文字を区別しないので、表記ゆれも塞ぐ。
func TestBlocksCaseInsensitive(t *testing.T) {
	check(t, hooktest.Deny,
		"git config CORE.HOOKSPATH hooks",
		"git config Core.HooksPath hooks",
		"git config core.hookspath hooks",
		"git config cOrE.hOoKsPaTh hooks",
		"git -c CORE.HOOKSPATH=x commit -m y",
	)
}

// 改行もコマンドの区切りである。読み取りと書き込みが混ざっても書き込みを拾う。
func TestBlocksNewlineSeparatedSegments(t *testing.T) {
	check(t, hooktest.Deny,
		"git config --get "+keyName+"\ngit config "+keyName+" hooks",
		"git config "+keyName+" hooks\ngit config --get "+keyName,
		"echo start\ngit config --unset "+keyName+"\necho done",
	)
}

// GIT_CONFIG_KEY_n 経由の設定は git config を通らない。
func TestBlocksEnvVarAssignment(t *testing.T) {
	check(t, hooktest.Deny,
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="+keyName+" GIT_CONFIG_VALUE_0=/tmp/h git commit -m x",
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_1="+keyName+" GIT_CONFIG_VALUE_1=x git status",
	)
}

// 明白なファイルの書き換えは、git config を経由しなくても止める。
func TestBlocksDirectGitConfigWritesFromBash(t *testing.T) {
	check(t, hooktest.Deny,
		"sed -i '' 's/bare = false/bare = true/' .git/config",
		"perl -pi -e 's/x/y/' /tmp/repo/.git/config",
		`printf '[core]\n' > .git/config`,
		`cat prepared-config >> "/tmp/repo/.git/config"`,
		"generate-config | tee .git/config",
		"truncate -s 0 .git/config",
		"cp prepared-config .git/config",
		"mv prepared-config /tmp/repo/.git/config",
		"rm .git/config",
		"dd if=prepared-config of=.git/config",
		"sed -i '' 's/x/y/' /tmp/repo/.git/worktrees/task/config.worktree",
		"cp prepared-config /tmp/repository.git/config",
		// sed の直後の空白がそのままフラグの前の空白になる形（移植元の先読みを展開した片方）。
		"sed -i s/x/y/ .git/config",
		"sed\t-i s/x/y/ .git/config",
	)
}

// Edit / Write は Git 設定ファイルへの直接の編集を拒否する。
func TestFileToolBlocksRepositoryConfigPaths(t *testing.T) {
	paths := []string{
		".git/config",
		"/tmp/repo/.git/config",
		"/tmp/repo/.git/config.worktree",
		"/tmp/repo/.git/worktrees/task/config.worktree",
		"/tmp/repo/.git/modules/child/config",
		"/tmp/repository.git/config",
		`C:\repo\.git\config`,
		// 移植元（Python）の $ は末尾の改行の直前にも一致する。
		"/tmp/repo/.git/config\n",
		"/tmp/repo/.git/config/",
	}
	for _, toolName := range []string{"Edit", "Write"} {
		for _, path := range paths {
			got := runRaw(t, fileToolPayload(toolName, path))
			if got.Decision != hooktest.Deny || !strings.Contains(got.Reason, "git config <key> <value>") {
				t.Errorf("%s %q: %+v", toolName, path, got)
			}
		}
	}
}

// 回避を思いとどまらせるのは理由文だけなので、迂回せずユーザーに伝えるよう促す（README の Limits）。
func TestReasonAsksToReportInsteadOfWorkingAround(t *testing.T) {
	got := runRaw(t, fileToolPayload("Edit", "/tmp/repo/.git/config"))
	if want := messages.T(hooktest.Language, idReason); got.Reason != want {
		t.Errorf("reason=%q, want %q", got.Reason, want)
	}
}

func TestAllowsReads(t *testing.T) {
	check(t, "",
		"git config "+keyName,
		"git config --get "+keyName,
		"git config --get-all "+keyName,
		"git config --local --get "+keyName,
		"git config --get "+keyName+" 2>/dev/null",
		"git config --get "+keyName+" > /tmp/out",
		"git config --get "+keyName+" >&2",
		"git config --get "+keyName+" && echo ok",
		"git config --get "+keyName+"; echo done",
		"git config --get "+keyName+` | tr -d '\n'`,
		"git config --list | grep "+keyName,
		"git config --list --show-origin | grep "+keyName,
		"git config --get "+keyName+" --show-origin",
		`test -n "$(git config `+keyName+`)" && echo set`,
		`[ -z "$(git config --get `+keyName+`)" ] || echo set`,
	)
}

func TestAllowsNoGitInvolved(t *testing.T) {
	check(t, "",
		"echo "+keyName,
		"grep -r "+keyName+" .",
		"rg '"+keyName+"' ~/.claude",
		"cat .git/config | grep -i "+strings.ToLower(keyName),
		`python3 -c 'print("`+keyName+`")'`,
		"echo 'set "+keyName+" manually'",
		"sed -i '' 's/"+keyName+"//' notes.md",
	)
}

func TestAllowsGitWithoutConfigWrite(t *testing.T) {
	check(t, "",
		"git log --grep "+keyName,
		"git grep "+keyName,
		`git config --get-regexp '^core\.'`,
		"git status",
		"git config --list",
		"ls -la",
	)
}

// リポジトリ固有の hook を .git/hooks に置く運用は塞がない。
func TestAllowsRepoLocalHooks(t *testing.T) {
	check(t, "",
		"cp scripts/pre-commit .git/hooks/pre-commit",
		"chmod +x .git/hooks/pre-push",
		"ls .git/hooks",
	)
}

func TestAllowsDirectGitConfigReads(t *testing.T) {
	check(t, "",
		"cat .git/config",
		"cat .git/config > /tmp/config-copy",
		"grep hooksPath /tmp/repo/.git/config",
		"sed -n '1,20p' .git/config",
		"git diff -- .git/config",
		"cp .git/config /tmp/config-backup",
		// sed の後ろに空白が無いものはその場編集とみなさない。
		"sedx -i s/x/y/ .git/config",
	)
}

// 対象外のツールと、似た名前のファイルは通す。
func TestFileToolAllowsNonConfigPathsAndOtherTools(t *testing.T) {
	cases := [][2]string{
		{"Edit", "/tmp/repo/git-config-notes.md"},
		{"Write", "/tmp/repo/.git/config.example"},
		{"Write", "/tmp/repo/config"},
		{"Read", "/tmp/repo/.git/config"},
		{"apply_patch", "/tmp/repo/.git/config"},
		{"MultiEdit", "/tmp/repo/.git/config"},
		{"NotebookEdit", "/tmp/repo/.git/config"},
	}
	for _, testCase := range cases {
		if got := runRaw(t, fileToolPayload(testCase[0], testCase[1])); got.Decision != "" {
			t.Errorf("%s %q: %+v", testCase[0], testCase[1], got)
		}
	}
}

// file_path で判定しないツールでも、command があれば Bash と同じに解析する。
func TestNonFileToolWithCommandIsParsedAsBash(t *testing.T) {
	for _, toolName := range []string{"apply_patch", "MultiEdit"} {
		raw := hooktest.ToolPayload(toolName, map[string]any{"command": "git config " + keyName + " hooks"}, "/tmp/repo")
		if got := runRaw(t, raw); got.Decision != hooktest.Deny {
			t.Errorf("%s with command: %+v", toolName, got)
		}
	}
}

func TestIsWrite(t *testing.T) {
	for segment, want := range map[string]bool{
		"git config " + keyName + " hooks":       true,
		"git config --unset " + keyName:          true,
		"git -c " + keyName + "=x commit":        true,
		"git config " + keyName + ` ""`:          true,
		"GIT_CONFIG_KEY_0=" + keyName + " git c": true,
		"GIT_CONFIG_KEY_10=" + keyName + " git":  true,
		"GIT_CONFIG_KEY_\u0661=" + keyName:       true,
		"git config " + keyName:                  false,
		"git config --get " + keyName:            false,
		"git config " + keyName + " 2>/dev/null": false,
		"git config " + keyName + " >out":        false,
		"git config --get " + keyName + " --x":   false,
		"echo " + keyName + " hooks":             false,
		"cat " + keyName:                         false,
		"git status":                             false,
		"GIT_CONFIG_KEY_0=" + keyName + "x git":  false,
		"git config " + keyName + " >/tmp/a":     false,
		"git config " + keyName + " >>/tmp/a":    false,
		"git config " + keyName + " 1>&2":        false,
	} {
		if got := isWrite(segment); got != want {
			t.Errorf("isWrite(%q)=%v, want %v", segment, got, want)
		}
	}
}

func TestIsGitConfigPath(t *testing.T) {
	for path, want := range map[string]bool{
		".git/config":                             true,
		"/repo/.git/config.worktree":              true,
		"/repo/.git/worktrees/wt/config.worktree": true,
		"/repo/.git/modules/sub/config":           true,
		"/repo.git/config":                        true,
		"/REPO/.GIT/CONFIG":                       true,
		".git/config.example":                     false,
		"/tmp/config":                             false,
		"/repo/git/config":                        false,
		"":                                        false,
		"/repo/.git/config\n\n":                   false,
	} {
		if got := isGitConfigPath(path); got != want {
			t.Errorf("isGitConfigPath(%q)=%v, want %v", path, got, want)
		}
	}
}

func TestIsDirectConfigWrite(t *testing.T) {
	for segment, want := range map[string]bool{
		"echo x > .git/config":      true,
		"sed -i s/x/y/ .git/config": true,
		"tee /repo/.git/config":     true,
		"cp x repo.git/config":      true,
		"cat .git/config":           false,
		"cat .git/config > /tmp/o":  false,
		"sed -n 1p .git/config":     false,
		"cp .git/config /tmp/out":   false,
		"dd if=.git/config of=/x":   false,
		"echo x 2>.git/config":      true,
	} {
		if got := isDirectConfigWrite(segment); got != want {
			t.Errorf("isDirectConfigWrite(%q)=%v, want %v", segment, got, want)
		}
	}
}

func TestOutputSchema(t *testing.T) {
	got := runRaw(t, hooktest.BashPayload("git config "+keyName+" hooks", "/tmp/repo", nil))
	if got.Decision != hooktest.Deny {
		t.Fatalf("%+v", got)
	}
	for _, part := range []string{".git/hooks/", "git config <key> <value>", "hooksPath"} {
		if !strings.Contains(got.Reason, part) {
			t.Errorf("reason %q does not contain %q", got.Reason, part)
		}
	}
}

func TestArgvDebugPath(t *testing.T) {
	if got := runArgv(t, "git config "+keyName+" hooks"); got.Decision != hooktest.Deny {
		t.Errorf("argv deny: %+v", got)
	}
	if got := runArgv(t, "git config --get "+keyName); got.Decision != "" {
		t.Errorf("argv allow: %+v", got)
	}
}

func TestOddInputsDoNotCrash(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "not json at all", "[]", "null",
		`{"tool_input": {}}`, `{"tool_input": {"command": null}}`,
		`{"tool_input": {"command": "core.hooksPath"}}`,
		`{"tool_input": {"command": 123}, "x": "core.hooksPath"}`,
		`{"tool_input": "git config core.hooksPath x"}`,
		`{"tool_name": "Write", "tool_input": {"file_path": 1}, "x": ".git/config"}`,
		`{"tool_name": 1, "tool_input": {"file_path": ".git/config"}}`,
	} {
		if got := runRaw(t, raw); got.Decision != "" {
			t.Errorf("odd input %q: %+v", raw, got)
		}
	}
}

func TestLongCommandIsHandled(t *testing.T) {
	padding := strings.Repeat("x", 20000)
	check(t, "", "echo "+padding)
	check(t, hooktest.Deny, "echo "+padding+"; git config "+keyName+" h")
}

func TestPathologicalInputIsFast(t *testing.T) {
	cases := []string{
		"git config " + keyName + " " + strings.Repeat("2>/dev/null ", 2000),
		"git config " + keyName + " " + strings.Repeat(`"" `, 2000),
		"git config " + strings.Repeat("x ", 5000) + keyName + " v",
		"git config " + keyName + " " + strings.Repeat("()", 2000),
	}
	start := time.Now()
	for _, command := range cases {
		runRaw(t, hooktest.BashPayload(command, "/tmp/repo", nil))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("pathological inputs took %s", elapsed)
	}
}
