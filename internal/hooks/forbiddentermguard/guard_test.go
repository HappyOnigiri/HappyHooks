package forbiddentermguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

// term は架空の禁止語である。テストには実際の語を書かない。
const term = "acme-internal"

// newRepo は語リストを持つリポジトリ（.git ディレクトリだけのもの）を作る。terms が空なら語リストを置かない。
func newRepo(t *testing.T, terms ...string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(terms) > 0 {
		writeFile(t, filepath.Join(repo, ".git", termsFileName), strings.Join(terms, "\n")+"\n")
	}
	return repo
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runIn(t *testing.T, cwd, command string) hooktest.Result {
	t.Helper()
	return hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, cwd, nil))
}

func TestBlocksPRAndIssueBodies(t *testing.T) {
	repo := newRepo(t, term)
	hooktest.CheckTable(t, Definition(), hooktest.Deny, repo, hooktest.Commands(
		`gh pr create --title "fix" --body "closes `+term+`#12"`,
		`gh pr create --body "`+term+`"`,
		`gh pr edit 12 --body "`+term+` の対応"`,
		`gh pr comment 12 --body "`+term+`"`,
		`gh issue create --title "`+term+`" --body "x"`,
		`gh issue comment 3 --body "`+term+`"`,
		`gh api repos/o/r/pulls -f body="`+term+`"`,
		`/opt/homebrew/bin/gh pr create --body "`+term+`"`,
		`cd sub && gh pr create --body "`+term+`"`,
		`gh pr create --body "`+strings.ToUpper(term)+`"`,
		"gh　pr create --body "+term,
		"gh\tapi repos/o/r/issues -f body="+term,
	))
}

// 移植元との違い: 読み取りの gh api は、語を含んでも本文を書き込まないので検査しない。
func TestAllowsReadOnlyAPI(t *testing.T) {
	repo := newRepo(t, term)
	hooktest.CheckTable(t, Definition(), "", repo, hooktest.Commands(
		"gh api repos/"+term+"/server --jq .visibility",
		"gh api user/memberships/orgs/"+term+" --jq '.state,.role' 2>&1 | head -3",
		`gh api "repos/`+term+`/server/pulls/1/files" --paginate --jq ".[].filename" | grep -E "\.rb$"`,
		"gh api -X GET search/issues -f q=repo:"+term+"/server",
		"gh api --method=get search/issues -f q="+term,
		"gh api -XGET search/issues -f q="+term,
		`gh api graphql -f query='query { repository(owner:"`+term+`", name:"server") { id } }'`,
		`gh api graphql -f query='query($sha:GitObjectID!){repository(owner:"`+term+`",name:"server"){object(oid:$sha){id}}}' -F sha=abc`,
		"cd ~/dev/"+term+" && gh api repos/"+term+"/server; git status | head -2",
		"/opt/homebrew/bin/gh api repos/"+term+"/server",
		"gh api repos/o/r && gh pr view 1 && echo "+term,
	))
}

// 読み取りと確かめられない gh api は、従来どおり検査する。
func TestDeniesAPIThatMayWrite(t *testing.T) {
	repo := newRepo(t, term)
	hooktest.CheckTable(t, Definition(), hooktest.Deny, repo, hooktest.Commands(
		"gh api repos/o/r/issues -f title="+term,
		"gh api repos/o/r/issues -F title="+term,
		"gh api repos/o/r/issues --raw-field=title="+term,
		"gh api -X POST repos/"+term+"/r/issues",
		"gh api --method PATCH repos/"+term+"/r/issues/1",
		"gh api repos/"+term+"/r/issues --input body.json",
		`gh api graphql -f query='mutation { addComment(input:{body:"`+term+`"}) { clientMutationId } }'`,
		"gh api graphql -F query=@q.graphql -f owner="+term,
		// 読み取りと書き込みが混ざれば検査する。
		"gh api repos/"+term+"/r; gh api repos/o/r/issues -f title=x",
		"gh api repos/"+term+"/r && gh pr create --body x",
		// gh の呼び出しが字句の上で見えない形は確かめられない。
		`echo "$(gh api repos/`+term+`/r)"`,
		"v=`date`; gh api repos/"+term+"/r",
		"xargs gh api repos/"+term+"/r",
		"GH_HOST=example.com gh api repos/"+term+"/r",
		"gh api repos/"+term+"/r --unknown-flag",
		`gh api 'repos/`+term+`/r`,
	))
}

func TestBodyFileContents(t *testing.T) {
	repo := newRepo(t, term)
	body := filepath.Join(repo, "body.md")
	writeFile(t, body, "## 概要\n\n"+term+" を直した\n")
	for _, command := range []string{
		"gh pr create --title x --body-file body.md",
		"gh pr create --title x --body-file " + body,
		"gh pr edit 12 --body-file=body.md",
		"gh issue comment 3 -F body.md",
		`gh issue comment 3 -F "body.md"`,
	} {
		got := runIn(t, repo, command)
		// 何行目かまで出す。
		if got.Decision != hooktest.Deny || !strings.Contains(got.Reason, ":3: "+term+" を直した") {
			t.Errorf("%q: %+v", command, got)
		}
	}
}

func TestBodyFileLimitsAndStdin(t *testing.T) {
	repo := newRepo(t, term)
	writeFile(t, filepath.Join(repo, "large.md"), term+"\n"+strings.Repeat("x", maxBodyBytes))
	for _, command := range []string{
		"gh pr create --body-file large.md",
		"gh pr create --body-file -",
		"gh pr create --body-file missing.md",
		"gh pr create --body-file .",
	} {
		if got := runIn(t, repo, command); got.Decision != "" {
			t.Errorf("%q: %+v", command, got)
		}
	}
}

func TestRegexEntry(t *testing.T) {
	repo := newRepo(t, "re:acme[-_ ]?corp")
	if got := runIn(t, repo, `gh pr create --body "AcmeCorp のこと"`); got.Decision != hooktest.Deny {
		t.Fatalf("%+v", got)
	}
}

// linked worktree からでも、共通の git ディレクトリの語リストを読む。
func TestLinkedWorktreeUsesCommonDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	git := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo,
			"-c", "user.name=hook-test", "-c", "user.email=hook-test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "init")
	writeFile(t, filepath.Join(repo, ".git", termsFileName), term+"\n")
	worktree := filepath.Join(root, "wt")
	git("worktree", "add", "-q", "--detach", worktree)
	if got := runIn(t, worktree, `gh pr create --body "`+term+`"`); got.Decision != hooktest.Deny {
		t.Fatalf("%+v", got)
	}
	// linked worktree の下のディレクトリからでも辿れる。
	sub := filepath.Join(worktree, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runIn(t, sub, `gh pr create --body "`+term+`"`); got.Decision != hooktest.Deny {
		t.Fatalf("%+v", got)
	}
}

func TestCommonDirResolution(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	writeFile(t, filepath.Join(common, "HEAD"), "ref: refs/heads/main\n")
	linked := filepath.Join(common, "worktrees", "wt")
	writeFile(t, filepath.Join(linked, "commondir"), "../..\n")
	// 相対の gitdir: と、CRLF の改行を解決する。
	writeFile(t, filepath.Join(root, "wt", ".git"), "gitdir: ../main/.git/worktrees/wt\r\n")
	writeFile(t, filepath.Join(root, "abs", ".git"), "gitdir: "+linked+"\n")
	writeFile(t, filepath.Join(root, "broken", ".git"), "not a gitdir line\n")
	writeFile(t, filepath.Join(root, "plain", ".keep"), "")
	for start, want := range map[string]string{
		filepath.Join(root, "main"):           common,
		filepath.Join(root, "main", "a", "b"): common,
		filepath.Join(root, "wt"):             common,
		filepath.Join(root, "abs"):            common,
		filepath.Join(root, "broken"):         "",
		"":                                    "",
	} {
		if err := os.MkdirAll(start, 0o755); start != "" && err != nil {
			t.Fatal(err)
		}
		if got := commonDir(start); got != want {
			t.Errorf("commonDir(%q)=%q, want %q", start, got, want)
		}
	}
	// commondir が無い（あるいは空の）git ディレクトリは、それ自体が共通のディレクトリである。
	writeFile(t, filepath.Join(root, "empty", ".git"), "gitdir: "+filepath.Join(root, "emptydir")+"\n")
	writeFile(t, filepath.Join(root, "emptydir", "commondir"), "\n")
	if got := commonDir(filepath.Join(root, "empty")); got != filepath.Join(root, "emptydir") {
		t.Errorf("empty commondir: %q", got)
	}
}

// 通す側。誤爆がこの hook の実害なので厚めに見る。
func TestAllowsCleanBodies(t *testing.T) {
	repo := newRepo(t, term)
	hooktest.CheckTable(t, Definition(), "", repo, hooktest.Commands(
		`gh pr create --title "fix" --body "バグを直した"`,
		"gh pr edit 12 --body-file body.md",
		"gh pr view 12",
		"gh pr list",
		"gh pr checkout 12",
		// 語の一部として続くサブコマンドは対象外（Python の \b と同じ語の境界）。
		"gh apix "+term,
		"gh apié "+term,
	))
}

// git の hook が受け持つ操作と、送信を伴わない操作は対象外。
func TestAllowsOtherCommandsWithTheTerm(t *testing.T) {
	repo := newRepo(t, term)
	hooktest.CheckTable(t, Definition(), "", repo, hooktest.Commands(
		`git commit -m "`+term+`"`,
		"git push origin feature/"+term,
		"grep -r "+term+" .",
		`echo "`+term+`" > note.txt`,
	))
}

// 語リストの無いリポジトリと、git の管理外では何もしない。
func TestOptIn(t *testing.T) {
	repo := newRepo(t)
	if got := runIn(t, repo, `gh pr create --body "`+term+`"`); got.Decision != "" {
		t.Errorf("repo without terms: %+v", got)
	}
	outside := t.TempDir()
	if got := runIn(t, outside, `gh pr create --body "`+term+`"`); got.Decision != "" {
		t.Errorf("outside a repository: %+v", got)
	}
	empty := newRepo(t, "# comment only", "", "re:")
	if got := runIn(t, empty, `gh pr create --body "`+term+`"`); got.Decision != "" {
		t.Errorf("terms file without terms: %+v", got)
	}
}

func TestParseTerms(t *testing.T) {
	terms, invalid := parseTerms("# comment\r\n\r\n  " + term + "  \rre:  foo\\d+bar \nre:(?<=x)y\nre:[unclosed\nRe:literal\n")
	for text, want := range map[string]bool{
		strings.ToUpper(term): true,
		"FOO12BAR":            true,
		"foobar":              false,
		// RE2 で使えない後読みは飛ばす。
		"xy":                   false,
		"[unclosed":            false,
		"re:literal":           true,
		"Re:LITERAL":           true,
		"# comment":            false,
		"  " + term + "  more": true,
		// Python の re.IGNORECASE は i・I・İ・ı を互いに一致させる。
		"acme-İnternal": true,
		"acme-ınternal": true,
		"ACME-İNTERNAL": true,
		"acme-nternal":  false,
	} {
		if got := matchesAny(text, terms); got != want {
			t.Errorf("matchesAny(%q)=%v, want %v", text, got, want)
		}
	}
	// コンパイルできない re: は語に含めず、行番号を返す。
	if len(terms) != 3 {
		t.Errorf("got %d terms, want 3", len(terms))
	}
	if len(invalid) != 2 || invalid[0] != 5 || invalid[1] != 6 {
		t.Errorf("invalid lines=%v, want [5 6]", invalid)
	}
}

// コンパイルできない re: の行があれば、ほかの語に該当しなくても対象のコマンドを拒否する。
func TestInvalidRegexpDenies(t *testing.T) {
	repo := newRepo(t, term, "re:(?<!x)lookbehind")
	got := runIn(t, repo, `gh pr create --body "clean"`)
	if got.Decision != hooktest.Deny {
		t.Fatalf("invalid pattern must deny: %+v", got)
	}
	termsPath := filepath.Join(repo, ".git", termsFileName)
	if want := invalidReason(hooktest.Language, []int{2}, termsPath); got.Reason != want {
		t.Errorf("reason=%q, want %q", got.Reason, want)
	}
	// 対象外のコマンドは語リストを読まない。
	if got := runIn(t, repo, `git commit -m "clean"`); got.Decision != "" {
		t.Errorf("non-target command: %+v", got)
	}
}

func TestReasonFormat(t *testing.T) {
	repo := newRepo(t, term)
	var lines []string
	for index := 0; index < 12; index++ {
		lines = append(lines, term)
	}
	lines = append(lines, strings.Repeat("あ", 250)+term)
	got := runIn(t, repo, "gh pr create --body '"+strings.Join(lines, "\n")+"'")
	if got.Decision != hooktest.Deny {
		t.Fatalf("%+v", got)
	}
	termsPath := filepath.Join(repo, ".git", termsFileName)
	// 該当箇所は 10 件まで並べ、残りの 3 件は件数だけを出す。
	findings := []string{"  command:1: gh pr create --body '" + term}
	for index := 2; index <= 13; index++ {
		findings = append(findings, "  command:"+strconv.Itoa(index)+": "+term)
	}
	if want := reason(hooktest.Language, findings, termsPath); got.Reason != want {
		t.Errorf("reason=%q, want %q", got.Reason, want)
	}
	if !strings.Contains(got.Reason, "  command:10: "+term+"\n"+messages.Text(hooktest.Language, idMore, map[string]any{"Count": 3})) {
		t.Errorf("reason %q must list 10 findings and count the rest", got.Reason)
	}
	if clipped := clip(strings.Repeat("あ", 250)); clipped != strings.Repeat("あ", 200)+" …" {
		t.Errorf("clip: %q", clipped)
	}
	if clipped := clip("  short  "); clipped != "short" {
		t.Errorf("clip: %q", clipped)
	}
}

func TestArgvPathUsesProcessCWD(t *testing.T) {
	repo := newRepo(t, term)
	t.Chdir(repo)
	if got := hooktest.Argv(t, Definition(), `gh pr create --body "`+term+`"`); got.Decision != hooktest.Deny {
		t.Errorf("argv: %+v", got)
	}
	// argv に payload の JSON を渡したときは payload として読む。
	payload := hooktest.BashPayload(`gh pr create --body "`+term+`"`, t.TempDir(), nil)
	if got := hooktest.Argv(t, Definition(), payload); got.Decision != "" {
		t.Errorf("argv payload must use its cwd: %+v", got)
	}
	// tool_input が object でない JSON は、移植元と同じく文字列全体をコマンドとして判定する。
	for _, raw := range []string{
		`{"tool_input": null, "x": "cd a && gh pr create --body ` + term + `"}`,
		`{"tool_input": " gh pr create --body ` + term + `"}`,
	} {
		if got := hooktest.Argv(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("argv %q: %+v", raw, got)
		}
	}
}

// payload の cwd が空なら、プロセスの cwd を使う。
func TestEmptyCWDFallsBackToProcessCWD(t *testing.T) {
	repo := newRepo(t, term)
	t.Chdir(repo)
	for _, cwd := range []string{`""`, "null", "0", "false"} {
		raw := `{"tool_input": {"command": "gh pr create --body ` + term + `"}, "cwd": ` + cwd + `}`
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("cwd %s: %+v", cwd, got)
		}
	}
	raw := `{"tool_input": {"command": "gh pr create --body ` + term + `"}}`
	if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
		t.Errorf("missing cwd: %+v", got)
	}
}

func TestOddInputsDoNotCrash(t *testing.T) {
	repo := newRepo(t, term)
	t.Chdir(repo)
	for _, raw := range []string{
		"", "   ", "gh pr create --body " + term, "[]", "null", "0", `"gh"`,
		`{"tool_input": {"command": null}, "x": "gh"}`,
		`{"tool_input": {"command": 123}, "x": "gh"}`,
		`{"tool_input": " gh pr create --body ` + term + `"}`,
		`{"tool_input": null, "x": "gh"}`,
		`{"tool_input": {"command": "gh pr create --body ` + term + `"}, "cwd": 1}`,
		`{"tool_input": {"command": "gh pr create --body ` + term + `"}, "cwd": ["x"]}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("odd input %q: %+v", raw, got)
		}
	}
}

func TestExpandUser(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	for path, want := range map[string]string{
		"~":                            "/Users/alice",
		"~/body.md":                    "/Users/alice/body.md",
		"/tmp/x":                       "/tmp/x",
		"relative/~":                   "relative/~",
		"~no-such-user-for-hhx-test/x": "~no-such-user-for-hhx-test/x",
	} {
		if got := expandUser(path); got != want {
			t.Errorf("expandUser(%q)=%q, want %q", path, got, want)
		}
	}
	// 相対パスは cwd に繋ぐので、先頭の ~ は展開されない（移植元と同じ）。
	files := bodyFiles("gh pr create --body-file ~/body.md -F /abs/x.md", "/work")
	if len(files) != 2 || files[0].path != "/work/~/body.md" || files[1].path != "/abs/x.md" {
		t.Errorf("bodyFiles: %+v", files)
	}
}

func TestDisabledByConfig(t *testing.T) {
	repo := newRepo(t, term)
	got := hooktest.Run(t, Definition(), hooktest.BashPayload("gh pr create --body "+term, repo, nil), hooktest.Options{
		Config: "hooks:\n  forbidden-term-guard:\n    enabled: false\n",
	})
	if got.Decision != "" {
		t.Fatalf("disabled hook must write nothing: %+v", got)
	}
}
