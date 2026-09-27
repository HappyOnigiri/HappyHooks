package dangerousrmguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

// 理由文に出るラベル（どの分岐が発火したかの判別用）。
var (
	labelEmptyVar     = commandLabel(idEmptyVar)
	labelCmdsub       = commandLabel(idCmdsub)
	labelTooMany      = messages.T(hooktest.Language, idTooMany)
	labelUnresolvable = messages.T(hooktest.Language, idUnresolvable)
	labelCritical     = messages.T(hooktest.Language, idCritical)
	labelCWD          = messages.T(hooktest.Language, idCWD)
	labelAncestor     = messages.T(hooktest.Language, idAncestor)
)

// commandLabel は rm と rmdir のどちらでも一致するよう、対象の表記のうちコマンド名で区切った長い方を返す。
func commandLabel(id string) string {
	longest := ""
	for _, part := range strings.Split(messages.Text(hooktest.Language, id, map[string]any{"Command": "\x00"}), "\x00") {
		if part = strings.TrimSpace(part); len(part) > len(longest) {
			longest = part
		}
	}
	return longest
}

// fakeHome は架空のホームディレクトリである。実行者の HOME で判定が変わらないように固定する。
const fakeHome = "/Users/alice"

// 調査のきっかけになった実物（worktree を 5 個作るスクリプト）。
const realWorld = `set -e
BASE=/private/tmp/scratchpad

mk_wt() {
  local name="$1"
  rm -rf "$BASE/$name"
  git worktree add -b "test-branch-$name" "$BASE/$name" HEAD --quiet
}

mk_wt test-a-clean-old
`

func TestMain(m *testing.M) {
	_ = os.Setenv("HOME", fakeHome)
	os.Exit(m.Run())
}

func check(t *testing.T, want string, cases []hooktest.Case) {
	t.Helper()
	hooktest.CheckTable(t, Definition(), want, "/tmp", cases)
}

func checkIn(t *testing.T, cwd, want string, cases []hooktest.Case) {
	t.Helper()
	hooktest.CheckTable(t, Definition(), want, cwd, cases)
}

func denyAll(label string, commands ...string) []hooktest.Case {
	cases := hooktest.Commands(commands...)
	for index := range cases {
		cases[index].Label = label
	}
	return cases
}

func reasonOf(t *testing.T, command, cwd string) string {
	t.Helper()
	got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, cwd, nil))
	if got.Decision != hooktest.Deny {
		t.Fatalf("command %q must be denied, got %+v", command, got)
	}
	return got.Reason
}

// realTempDir は symlink を解決した一時ディレクトリを返す（macOS の /var は /private/var への symlink）。
func realTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

// --- 空になりうる変数を対象にした削除（組み込みの lal / A6V） ---

func TestEmptyVariableRealWorldCase(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{{Command: realWorld, Label: `"$BASE/$name"`}})
}

func TestEmptyVariableForms(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelEmptyVar,
		`rm -rf "$BASE/$name"`, "rm -rf $BASE/$name", "rm -rf ${BASE}/$name", `rm -rf "${BASE}/${name}"`,
	))
	// `rm -rf $UNSET/*` → `rm -rf /*` に化ける形。
	check(t, hooktest.Deny, hooktest.Commands("rm -rf $UNSET/*", `rm -rf "$UNSET/*"`, "rm -rf ${UNSET}/*", "rm -r $DIR/**"))
	check(t, hooktest.Deny, hooktest.Commands(`rm -rf "$DIR/"`, "rm -rf $DIR/", "rm -rf $DIR//sub", "rmdir $DIR/"))
	// `rmdir -p` + glob は先に「静的に解決できない」側で捕まる。
	check(t, hooktest.Deny, denyAll(labelEmptyVar, "rmdir $DIR/*", "rmdir -- ${DIR}/*"))
}

func TestDerivedVariableRemoval(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelEmptyVar,
		"S=/private/tmp/scratchpad; for n in a b; do D=$S/$n; rm -rf $D; git clone /tmp/source $D; done",
		`D="$BASE/$name"; rm -rf "$D"`,
		"D=$BASE/$name; rm -rf ${D}",
	))
	reason := reasonOf(t, "D=$BASE/$name; rm -rf $D", "/tmp")
	if !strings.Contains(reason, messages.T(hooktest.Language, idWhyDerived)) ||
		!strings.Contains(reason, messages.Text(hooktest.Language, idHowDerived, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("derived variable reason: %q", reason)
	}
	check(t, "", hooktest.Commands(
		"rm -rf $D",
		"D=/tmp/work/a; rm -rf $D",
		"D=$BASE/$name; D=/tmp/work/a; rm -rf $D",
		"rm -rf $D; D=$BASE/$name",
		"D=$BASE/build; rm -rf $D",
	))
}

// 先行代入・パス指定・エスケープつきの起動と、シェルの区切り。
func TestEmptyVariableInvocationForms(t *testing.T) {
	check(t, hooktest.Deny, hooktest.Commands(
		"TMPDIR=/tmp rm -rf $DIR/*", "FOO=1 BAR=2 rm -rf $DIR/*", "/bin/rm -rf $DIR/*", "./rm -rf $DIR/*",
		`\rm -rf $DIR/*`, "A+=x rm -rf $DIR/*",
		"echo start && rm -rf $DIR/*", "echo start; rm -rf $DIR/*", "echo start\nrm -rf $DIR/*",
		"echo start | rm -rf $DIR/*", "false || rm -rf $DIR/*", "rm -rf $DIR/* &", "(rm -rf $DIR/*)",
		"{ rm -rf $DIR/*; }", "echo start\rrm -rf $DIR/*", "rm　-rf $DIR/*",
	))
}

func TestEmptyVariableOptionsAndRedirects(t *testing.T) {
	check(t, hooktest.Deny, hooktest.Commands(
		"rm -rf -- $DIR/*", "rm -rf 2>/dev/null $DIR/*", "rm -rf 2> /dev/null $DIR/*", "rm -rf $DIR/* > log.txt",
	))
	check(t, hooktest.Deny, denyAll(labelEmptyVar, "rm -rf \\\n  $DIR/*"))
}

// 案内する `${BASE:?}` は固定名までしか続けられない。そこまで書いてあること。
// `:?` の `?` が glob 文字として数えられるため、glob を続けると別分岐で止まる。
func TestEmptyVariableReasonBoundsTheGuardedExpansion(t *testing.T) {
	reason := reasonOf(t, `rm -rf "$BASE/$name"`, "/tmp")
	for _, part := range []string{"${BASE:?}", messages.Text(hooktest.Language, idHowEmptyVar, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})} {
		if !strings.Contains(reason, part) {
			t.Errorf("reason does not contain %q", part)
		}
	}
	check(t, "", hooktest.Commands(`rm -rf "${BASE:?}"/work`))
	check(t, hooktest.Deny, denyAll(labelUnresolvable, `rm -rf "${BASE:?}"/*`))
}

// --- コマンド置換まわり（組み込みの g9v） ---

func TestSubstitution(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelCmdsub, "echo $(rm -rf $D/*)", "echo `rm -rf $D/*`", `echo "$(rm -rf ${D}/$name)"`))
	check(t, hooktest.Deny, denyAll(labelTooMany, "rm -rf ./build "+strings.Repeat("$(true) ", 65)))
	check(t, "", hooktest.Commands("rm -rf ./build "+strings.Repeat("$(true) ", 10)))
}

// `"$(pwd)/build"` を `/build` と読んで「最上位ディレクトリの削除」にしないこと。
func TestSubstitutionIsNotMistakenForALiteralPath(t *testing.T) {
	check(t, "", hooktest.Commands(
		`rm -rf "$(pwd)/build"`, `rm -rf "$(git rev-parse --show-toplevel)/tmp"`, "rm -rf \"`pwd`/build\"",
		`rm -rf "$(mktemp -d)/x"`, "rm -rf $(cat /tmp/path.txt)",
	))
}

// 置換 + 末尾 glob は組み込みが bypass 免疫の ask を出す形（V7r 分岐 B の `e_(p)`）。
func TestSubstitutionWithTrailingGlobIsDenied(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelUnresolvable,
		`rm -rf "$(pwd)"/*`, "rm -rf $(git rev-parse --show-toplevel)/tmp/*", "rm -rf \"`pwd`\"/*",
	))
	reason := reasonOf(t, `rm -rf "$(pwd)"/*`, "/tmp")
	if !strings.Contains(reason, messages.Text(hooktest.Language, idHowCmdsubGlob, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("reason must point at the substitution: %q", reason)
	}
	// 内部トークンは書いた覚えのない文字列なので、`$(…)` に戻して見せる。
	if strings.Contains(reason, cmdsubToken) || !strings.Contains(reason, "$(…)") {
		t.Errorf("reason must hide the internal token: %q", reason)
	}
	// 置換を潰しても、同じ断片の別の対象は見落とさない。
	check(t, hooktest.Deny, denyAll(labelCritical, `rm -rf "$(pwd)/build" /usr`))
}

// --- 静的に解決できない削除対象（組み込みの V7r 分岐 A / B / D） ---

func TestUnresolvableTargets(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelUnresolvable,
		// 分岐 A: cd 後の相対 glob は最終的な作業ディレクトリが確定しない。
		"cd sub && rm -rf ./*", "cd /tmp/work && rm -rf *", "pushd sub; rm -rf ./*",
		// 分岐 B: `..` で上に抜ける形、`~user` 形、`*/` で終わる相対指定、rmdir -p。
		"rm -rf ./sub/../*", "rm -rf ~someone/*", "rm -rf dist/*/", "rmdir -p build/*", "rmdir --parents build/*",
		"rmdir -p ${DIR}/*",
		// 分岐 D: 列挙できない階層を跨ぐ glob。
		"rm -rf a/*/b/*", "rm -rf ./*/*",
	))
	// 1 階層だけの glob は組み込みも ask にしない。
	check(t, "", hooktest.Commands(
		"rm -rf node_modules/*", "rm -rf ./build/*", "rm -rf /tmp/work/foo/*",
		"rm -rf tmp/*/cache", // glob が 1 つなら階層が深くても通る
		"rm -rf tmp/*",       // 分岐 A の案内どおりに書き直したもの
	))
}

// A / B / D で理由と対処を分けること。
func TestUnresolvableReasonIsSpecificPerBranch(t *testing.T) {
	cdReason := reasonOf(t, "cd sub && rm -rf ./*", "/tmp")
	if !strings.Contains(cdReason, messages.Text(hooktest.Language, idHowCD, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) || !strings.Contains(cdReason, "rm -rf tmp/*") {
		t.Errorf("cd reason: %q", cdReason)
	}
	// 基準の作業ディレクトリが不定なのだから、解決後のパスを断定して見せない。
	if strings.Contains(strings.SplitN(cdReason, "\n", 2)[0], "→") {
		t.Errorf("cd reason must not show a resolved path: %q", cdReason)
	}
	shapeReason := reasonOf(t, "rmdir -p build/*", "/tmp")
	if !strings.Contains(shapeReason, "`-p`") || !strings.Contains(shapeReason, messages.Text(hooktest.Language, idHowShape, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("shape reason: %q", shapeReason)
	}
	if globReason := reasonOf(t, "rm -rf a/*/b/*", "/tmp"); !strings.Contains(globReason, messages.Text(hooktest.Language, idHowGlob, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("glob reason: %q", globReason)
	}
}

// --- システム重要ディレクトリ（組み込みの Kar） ---

func TestCriticalPaths(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelCritical,
		"rm -rf /", "rm -rf /*", "rm -rf /usr", "rm -rf /etc/", "rm -rf /opt", "rm -rf /usr/*", "rm -rf //usr",
		"rm -rf /private/etc", "rm -rf /PRIVATE/TMP",
		"rm -rf ~", "rm -rf ~/", "rm -rf ~/*",
		// 組み込みが実値へ展開する唯一の変数。`Wr()` の `if(s==="HOME") return Am()` に対応。
		"rm -rf $HOME", `rm -rf "$HOME"`, "rm -rf $HOME/", "rm -rf $HOME/*", "rm -rf "+fakeHome, "rm -rf /users/ALICE",
		// `${HOME}` は組み込みが too-complex に落とす形。ask かは未確認だが deny 側へ寄せる。
		"rm -rf ${HOME}", `rm -rf "${HOME}"`,
		// 再代入は追わない（組み込みもこの形は追えず critical path の ask を出す）。
		"S=/tmp/sandbox; export HOME=$S/home; rm -rf $HOME",
	))
	// `~` や `${HOME}` は解決後のパスまで出す（`}` を閉じ括弧として落としてから展開すると一致しなくなる）。
	for _, command := range []string{"rm -rf ~", "rm -rf ${HOME}"} {
		if reason := reasonOf(t, command, "/tmp"); !strings.Contains(reason, fakeHome) {
			t.Errorf("%q: reason must show the home directory: %q", command, reason)
		}
	}
	check(t, "", hooktest.Commands(
		"S=/tmp/sandbox; rm -rf /tmp/sandbox/home",
		// 既知変数だけを展開する。名前が前方一致するだけの変数を巻き込まない。
		"rm -rf $HOMEBREW_PREFIX/x", "rm -rf ${HOME}x/y", "rm -rf $HOME/.cache/mytool", `rm -rf "$HOME/.cache/mytool/*"`,
		"rm -rf /tmp/work/foo", "rm -rf /usr/local/share/mything", "rm -f ~/.cache/mytool/x.log",
	))
}

// 止める前に「消さずに済ます」「範囲を狭める」を順に提示していること。
func TestCriticalReasonOffersAlternativesBeforeStopping(t *testing.T) {
	reason := reasonOf(t, "rm -rf /usr", "/tmp")
	for _, part := range []string{messages.Text(hooktest.Language, idHowCritical, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)}), "echo", "`/usr/*`"} {
		if !strings.Contains(reason, part) {
			t.Errorf("reason does not contain %q", part)
		}
	}
}

// ホームディレクトリは HOME の値でも、symlink を解決した実体でも照合する。
func TestCriticalHomeThroughSymlink(t *testing.T) {
	root := realTempDir(t)
	realPath := filepath.Join(root, "real-home")
	link := filepath.Join(root, "home")
	if err := os.Mkdir(realPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link+"/")
	check(t, hooktest.Deny, denyAll(labelCritical, "rm -rf "+realPath, "rm -rf "+link, "rm -rf ~", "rm -rf $HOME/"))
	check(t, "", hooktest.Commands("rm -rf "+realPath+"/x"))
}

// --- 作業ディレクトリとその祖先（組み込みの MF） ---

func TestWorkspace(t *testing.T) {
	root := realTempDir(t)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(deep, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkIn(t, deep, hooktest.Deny, denyAll(labelAncestor, "rm -rf ..", "rm -rf ../..", "rm -rf "+root+"/a",
		"rm -rf "+root+"/a/b"))
	checkIn(t, deep, hooktest.Deny, denyAll(labelCWD, "rm -rf .", "rm -rf *", "rm -rf "+deep, "rm -rf "+deep+"/"))
	// 組み込みは PWD をセンチネルに置き換えるだけで実値に展開しない（ask にならない）。
	checkIn(t, deep, "", hooktest.Commands("rm -rf $PWD", "rm -rf ${PWD}", "rm -rf $PWD/sub"))
	checkIn(t, deep, "", hooktest.Commands("rm -rf ./sub", "rm -rf sub", "rm -rf "+deep+"/sub", "rm -f ./sub/x.log"))
}

// cwd が symlink 越しでも、実体のパスで書いた祖先を見つける。
func TestWorkspaceThroughSymlink(t *testing.T) {
	root := realTempDir(t)
	deep := filepath.Join(root, "work", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "work"), link); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(link, "a", "b")
	checkIn(t, cwd, hooktest.Deny, denyAll(labelAncestor, "rm -rf "+filepath.Join(root, "work", "a"), "rm -rf "+link))
	checkIn(t, cwd, hooktest.Deny, denyAll(labelCWD, "rm -rf "+deep))
}

func TestWorkspaceReasons(t *testing.T) {
	root := realTempDir(t)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// cwd そのものは「実体パスで列挙して再実行してよい」側にする。
	if reason := reasonOf(t, "rm -rf *", deep); !strings.Contains(reason, messages.Text(hooktest.Language, idHowCWDItself, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("cwd reason: %q", reason)
	}
	// cwd ごと消したい最頻ケースは worktree の後始末なので、専用コマンドを先に出す。
	if reason := reasonOf(t, "rm -rf "+deep, deep); !strings.Contains(reason, "worktree remove") {
		t.Errorf("cwd reason: %q", reason)
	}
	// `./*` は中身だけなので「作業ディレクトリごと消える」と断定しない。
	if reason := reasonOf(t, "rm -rf ./*", deep); !strings.Contains(reason, messages.T(hooktest.Language, idWhyCWD)) {
		t.Errorf("cwd reason: %q", reason)
	}
	// `..` は解決後のパスまで出し、cd で判定を外す形の禁止まで書いてある。
	reason := reasonOf(t, "rm -rf ..", deep)
	for _, part := range []string{filepath.Dir(deep), "worktree remove", "cd", messages.Text(hooktest.Language, idHowAncestor, map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})} {
		if !strings.Contains(reason, part) {
			t.Errorf("ancestor reason does not contain %q: %q", part, reason)
		}
	}
}

// --- 通過側 ---

func TestAllowed(t *testing.T) {
	check(t, "", hooktest.Commands(
		"rm -rf /tmp/work/foo", "rm -rf ./build", "rm -rf node_modules", "rm -f /tmp/$name.log",
		// `$VAR/` の直後が普通の文字。組み込みも ask にしない形。
		`rm -rf "$BASE/blob.txt"`, "rm -rf $BASE/build", "rm -f ${BASE}/tmp.log",
		// 理由文で案内する書き直し先が、実際に通ること。
		`rm -rf "${BASE:?}/$name"`, `rm -rf "${BASE:?BASE unset}/$name"`, "rm -rf ${BASE:-/tmp/fallback}/*",
		"rm -f $file", `rm -rf "$DIR"`, "rm -rf $DIR*",
		// 削除コマンドではない。
		`echo "$BASE/$name"`, `ls "$BASE/"*`, "git worktree remove --force $BASE/$name",
		"terraform destroy -target=$MOD/*", // "terraform" が一次ゲートの "rm" を含む
		`cat "$DIR/"*.log`, "rm --help",
		"npm run build:$TARGET/*", "confirm_rm $DIR/*", "echo rm -rf $DIR/*", "rmé $DIR/*",
		"ls -la", "git status",
	))
}

// --- L2: 関数単位 ---

func TestFindDangerousRemoval(t *testing.T) {
	for command, want := range map[string][2]string{
		`rm -rf "$BASE/$name"`: {"rm", `"$BASE/$name"`},
		"rmdir ${D}/*":         {"rmdir", "${D}/*"},
		"rm -rf $BASE/build":   {"", ""},
		// 外側の走査では置換の中身を見ない（中身は findInSubstitution の担当）。
		"echo $(rm -rf $D/*)": {"", ""},
	} {
		if name, target := findDangerousRemoval(command); [2]string{name, target} != want {
			t.Errorf("findDangerousRemoval(%q)=(%q, %q), want %q", command, name, target, want)
		}
	}
	if name, _ := findInSubstitution("echo $(rm -rf $D/*)"); name != "rm" {
		t.Errorf("findInSubstitution must see inside the substitution")
	}
}

// A6V が受ける「`$VAR/` の直後」の文字集合。
func TestA6VBoundary(t *testing.T) {
	for tail, want := range map[string]bool{
		"*": true, "$": true, "/": true, `"`: true, "'": true, "": true, "a": false, ".": false, "-": false, "\n": false,
	} {
		if got := a6vRE.MatchString("$V/" + tail); got != want {
			t.Errorf("A6V($V/%q)=%v, want %v", tail, got, want)
		}
	}
}

func TestL6V(t *testing.T) {
	for head, want := range map[string]bool{
		"A=1 B=2 rm -rf x": true, "/usr/bin/rmdir x": true, "rm": true, "rm　x": true, `\rm x`: true,
		"confirm_rm x": false, "echo rm x": false, "rmx": false, "a=b/rm x": false,
	} {
		if got := l6vRE.MatchString(head); got != want {
			t.Errorf("L6V(%q)=%v, want %v", head, got, want)
		}
	}
}

func TestIsCriticalPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/": true, "/usr": true, "/etc/": true, fakeHome: true, "/*": true, "*": true, "/private/var": true,
		"/usr/local/share/x": false, fakeHome + "/.cache": false, "/private/varx/y": false,
	} {
		if got, err := isCriticalPath(path); err != nil || got != want {
			t.Errorf("isCriticalPath(%q)=%v, %v; want %v", path, got, err, want)
		}
	}
}

func TestRemovesWorkspace(t *testing.T) {
	base := "/tmp/work/a/b"
	for target, want := range map[string]bool{
		"/tmp/work/a/b": true, "/tmp/work/a": true, "/tmp": true, "/TMP/Work": true, "/private/tmp/work": true,
		"/tmp/work/a/b/c": false, "/other": false,
	} {
		if got, err := removesWorkspace(base, target); err != nil || got != want {
			t.Errorf("removesWorkspace(%q, %q)=%v, %v; want %v", base, target, got, err, want)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	for path, want := range map[string]bool{"sub/../*": true, "./a/..": true, "../../x": false, "a/b": false} {
		if got := escapesUpward(path); got != want {
			t.Errorf("escapesUpward(%q)=%v, want %v", path, got, want)
		}
	}
	for path, want := range map[string]string{
		"/a/b/*": "/a/b", "/a/b/*/": "/a/b", "/a/b/**": "/a/b", "/*": "/", "/a/b": "/a/b", "/a/*/*": "/a",
		"a/*": "a", "*": "*", "/a/*\n": "/a\n",
	} {
		if got := stripGlobTail(path); got != want {
			t.Errorf("stripGlobTail(%q)=%q, want %q", path, got, want)
		}
	}
	for path, want := range map[string]string{
		"/private/tmp": "/tmp", "/private/var/x": "/var/x", "/PRIVATE/Etc/": "/Etc/", "/private/tmp\n": "/tmp\n",
		"/private/tmpx": "/private/tmpx", "/private/opt": "/private/opt", "/x/private/tmp": "/x/private/tmp",
	} {
		if got := alias(path); got != want {
			t.Errorf("alias(%q)=%q, want %q", path, got, want)
		}
	}
}

func TestArgumentHelpers(t *testing.T) {
	for args, want := range map[string][]string{
		"-rf a b": {"a", "b"}, "-rf -- -x": {"-x"}, "-rf": nil, "- -x": {"-", "-x"}, "a -rf b": {"a", "-rf", "b"},
	} {
		if got := positionalArgs(strings.Fields(args)); !reflect.DeepEqual(got, want) {
			t.Errorf("positionalArgs(%q)=%q, want %q", args, got, want)
		}
	}
	for args, want := range map[string][]string{
		"a 2> f b": {"a", "b"}, "a 2>f b": {"a", "b"}, "a >> f": {"a"}, "&> f a": {"a"}, "<<< x a": {"a"},
	} {
		if got := dropRedirects(strings.Fields(args)); !reflect.DeepEqual(got, want) {
			t.Errorf("dropRedirects(%q)=%q, want %q", args, got, want)
		}
	}
	for command, want := range map[string]bool{
		"cd sub && rm -rf ./*": true, "pushd sub; rm -rf ./*": true, "X=1 cd x": true, "(cd x)": true,
		"rm -rf ./*": false, "echo cd sub": false, "cdx": false,
	} {
		if got := hasDirectoryChange(command); got != want {
			t.Errorf("hasDirectoryChange(%q)=%v, want %v", command, got, want)
		}
	}
}

// 先読み・後読みを使っていた置き換えを、同じ結果になる走査にしたもの。
func TestLookaroundReplacements(t *testing.T) {
	for text, want := range map[string]string{
		"a (b) c": "a   c", "$(b) (c)": "$(b)  ", "((a) b)": "(  b)", "(a(b)": "(a ", "x(": "x(", "()()": "  ",
	} {
		if got := replaceParens(text, " "); got != want {
			t.Errorf("replaceParens(%q)=%q, want %q", text, got, want)
		}
	}
	for text, want := range map[string]string{
		"a & b": "a ; b", "a && b": "a && b", "a &> f": "a &> f", "2>&1 &": "2>&1 ;", "&": ";", "<&x": "<&x",
	} {
		if got := replaceBackgroundAmp(text); got != want {
			t.Errorf("replaceBackgroundAmp(%q)=%q, want %q", text, got, want)
		}
	}
	for token, want := range map[string]string{
		"$HOME": fakeHome, "${HOME}/x": fakeHome + "/x", "$HOME/x": fakeHome + "/x", "$HOMEx": "$HOMEx",
		"$HOME_x": "$HOME_x", "$HOME-x": fakeHome + "-x", "a$HOME$HOME": "a" + fakeHome + fakeHome, "x": "x",
		"$HOMEé": fakeHome + "é",
	} {
		if got := expandKnownVars(token); got != want {
			t.Errorf("expandKnownVars(%q)=%q, want %q", token, got, want)
		}
	}
}

// 移植元の CMD_SUBST（`\$\(([^()]*)\)`）の置き換えを走査にしたもの。期待値は Python の re.sub で確かめた。
func TestReplaceCmdSubst(t *testing.T) {
	for text, want := range map[string]string{
		"$(a) $(b(c)) $$(d)": "X $(b(c)) $X", "$($(x))": "$(X)", "$(": "$(", "a$(b": "a$(b", "$()$()": "XX",
	} {
		if got := replaceCmdSubst(text, "X"); got != want {
			t.Errorf("replaceCmdSubst(%q)=%q, want %q", text, got, want)
		}
	}
}

func TestSubstitutions(t *testing.T) {
	got := substitutions("a `b` $(c $(d)) `e`")
	if want := []string{"b", "e", "d", "c  "}; !reflect.DeepEqual(got, want) {
		t.Errorf("substitutions=%q, want %q", got, want)
	}
}

// --- 入出力の契約 ---

func TestArgvPathMatchesStdinPath(t *testing.T) {
	if got := hooktest.Argv(t, Definition(), `rm -rf "$BASE/$name"`); got.Decision != hooktest.Deny {
		t.Errorf("argv deny: %+v", got)
	}
	if got := hooktest.Argv(t, Definition(), "rm -rf $BASE/build"); got.Decision != "" {
		t.Errorf("argv allow: %+v", got)
	}
}

// argv の経路では、cwd に依存する判定にプロセスの作業ディレクトリを使う。
func TestArgvUsesTheProcessWorkingDirectory(t *testing.T) {
	directory := realTempDir(t)
	t.Chdir(directory)
	if got := hooktest.Argv(t, Definition(), "rm -rf ."); !strings.Contains(got.Reason, labelCWD) {
		t.Errorf("argv cwd: %+v", got)
	}
}

func TestPayloadFrom(t *testing.T) {
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	processCWD, err = filepath.EvalSymlinks(processCWD)
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string][2]string{
		`{"tool_input": {"command": "rm x"}, "cwd": "/w"}`: {"rm x", "/w"},
		`{"tool_input": {"command": "rm x"}, "cwd": ""}`:   {"rm x", processCWD},
		`{"tool_input": {"command": "rm x"}, "cwd": 1}`:    {"rm x", processCWD},
		`{"tool_input": {"command": 1}, "cwd": "/w"}`:      {"", "/w"},
		`{"tool_input": null}`:                             {"", processCWD},
		// JSON として読めない入力と object でない JSON は、生のまま判定に回す（fail-open にしない）。
		"rm -rf /": {"rm -rf /", processCWD},
		"null":     {"null", processCWD},
		`["rm"]`:   {`["rm"]`, processCWD},
	} {
		command, cwd, err := payloadFrom([]byte(raw))
		if err != nil || [2]string{command, cwd} != want {
			t.Errorf("payloadFrom(%q)=(%q, %q, %v), want %q", raw, command, cwd, err, want)
		}
	}
}

func TestBrokenJSONIsNotFailOpen(t *testing.T) {
	for _, raw := range []string{"rm -rf /", "rm -rf $D/*", `{"tool_input": {}} ; rm -rf /usr`} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("broken input %q: %+v", raw, got)
		}
	}
}

func TestMalformedPayloadDoesNotCrash(t *testing.T) {
	for _, value := range []any{
		nil, map[string]any{"tool_input": nil}, map[string]any{"tool_input": map[string]any{}},
		map[string]any{"tool_input": map[string]any{"command": nil}},
		map[string]any{"tool_input": map[string]any{"command": []any{"rm -rf /"}}},
		map[string]any{"tool_input": map[string]any{"command": "rm -rf ."}, "cwd": nil},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		hooktest.Stdin(t, Definition(), string(raw))
	}
	for _, raw := range []string{"", "[]", "not json rm"} {
		hooktest.Stdin(t, Definition(), raw)
	}
}

// NUL を含むパスは、移植元では os.lstat の ValueError で落ちて無出力だった。
func TestNULInPathIsSilent(t *testing.T) {
	for _, raw := range []string{
		`{"tool_input": {"command": "rm -rf ./x\u0000"}, "cwd": "/tmp"}`,
		`{"tool_input": {"command": "rm -rf ."}, "cwd": "/tmp/\u0000x"}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("%s: %+v", raw, got)
		}
	}
	// realpath より先に判定する分岐（静的に解決できない対象）は、移植元と同じく拒否する。
	raw := `{"tool_input": {"command": "rm -rf a/*/b\u0000/*"}, "cwd": "/tmp"}`
	if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
		t.Errorf("%s: %+v", raw, got)
	}
}

func TestLongCommandIsHandled(t *testing.T) {
	padding := strings.Repeat("x", 20000)
	check(t, "", hooktest.Commands("echo "+padding))
	check(t, hooktest.Deny, hooktest.Commands("echo "+padding+"; rm -rf $D/*"))
}

// 病的に長い入力でも括弧の潰し込みが破綻しない。
func TestPathologicalInputIsFast(t *testing.T) {
	cases := []string{
		"rm -rf $D/* " + strings.Repeat("2>/dev/null ", 2000),
		"rm -rf " + strings.Repeat("$D/x ", 2000) + "$D/*",
		"echo " + strings.Repeat("()", 2000) + "; rm -rf $D/*",
		"echo " + strings.Repeat("(", 2000) + strings.Repeat(")", 2000) + "; rm -rf x",
		"echo " + strings.Repeat("`x` ", 2000) + "; rm -rf x",
		"rm -rf " + strings.Repeat("a/", 2000) + "*",
		"echo " + strings.Repeat("$(", 2000) + strings.Repeat(")", 2000) + "; rm -rf x",
	}
	start := time.Now()
	for _, command := range cases {
		hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

func TestPrimaryGate(t *testing.T) {
	for input, want := range map[string]bool{"ls -la": false, "git status": false, "rm x": true, "terraform": true} {
		if got := gate([]byte(input)); got != want {
			t.Errorf("gate(%q)=%v, want %v", input, got, want)
		}
	}
}

func TestDisabledByConfig(t *testing.T) {
	got := hooktest.Run(t, Definition(), hooktest.BashPayload("rm -rf /", "/tmp", nil),
		hooktest.Options{Config: "hooks:\n  dangerous-rm-guard:\n    enabled: false\n"})
	if got.Decision != "" {
		t.Fatalf("disabled hook must stay silent: %+v", got)
	}
}
