package dangerousrmguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

// 実在しないリンク先を使い、OS の /private 配下の構成に依存せず別名との照合を検証する。
func TestCriticalHomeThroughPrivateAlias(t *testing.T) {
	for _, directory := range []string{"tmp", "var", "etc", "home"} {
		t.Run(directory, func(t *testing.T) {
			realPath := "/private/" + directory + "/hhx-test/real-home"
			aliasPath := "/" + directory + "/hhx-test/real-home"
			link := filepath.Join(t.TempDir(), "home")
			if err := os.Symlink(realPath, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", link+"/")
			check(t, hooktest.Deny, denyAll(labelCritical,
				"rm -rf "+realPath, "rm -rf "+aliasPath, "rm -rf "+link, "rm -rf ~", "rm -rf $HOME/"))
			check(t, "", hooktest.Commands("rm -rf "+realPath+"/x", "rm -rf "+aliasPath+"/x"))
		})
	}
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

// pathologicalInputs は機能テストとベンチマークで同じ長い入力を使う。
func pathologicalInputs() []struct {
	name    string
	command string
	want    string
	label   string
} {
	return []struct {
		name    string
		command string
		want    string
		label   string
	}{
		{"redirects", "rm -rf $D/* " + strings.Repeat("2>/dev/null ", 2000), hooktest.Deny, labelEmptyVar},
		{"variable_paths", "rm -rf " + strings.Repeat("$D/x ", 2000) + "$D/*", hooktest.Deny, labelEmptyVar},
		{"adjacent_parens", "echo " + strings.Repeat("()", 2000) + "; rm -rf $D/*", hooktest.Deny, labelEmptyVar},
		{"nested_parens", "echo " + strings.Repeat("(", 2000) + strings.Repeat(")", 2000) + "; rm -rf x", "", ""},
		{"backticks", "echo " + strings.Repeat("`x` ", 2000) + "; rm -rf x", hooktest.Deny, labelTooMany},
		{"deep_path", "rm -rf " + strings.Repeat("a/", 2000) + "*", "", ""},
		{"nested_substitutions", "echo " + strings.Repeat("$(", 2000) + strings.Repeat(")", 2000) + "; rm -rf x", hooktest.Deny, labelTooMany},
		// shell -c の分岐。外側の cd の有無を shell -c ごとに調べ直すと、入力の長さの 2 乗になる。
		{"inline_many_shells", strings.Repeat("bash -c 'x=1; $X'; ", 3000), "", ""},
		{"inline_long_script", "bash -c '" + strings.Repeat("$X; ", 5000) + "'", hooktest.Deny, labelInlineUnchecked},
		{"inline_many_positional", "bash -c 'echo " + strings.Repeat("$1", 900) + "' _ a", "", ""},
		{"inline_many_braces", "bash -c 'echo " + strings.Repeat("{a,$b}", 5000) + "'", "", ""},
		// 読み切れない `${…}` の分岐。閉じない添字を繰り返しても、添字を末尾まで読み直さない。
		{"unclosed_subscripts", "echo " + strings.Repeat("${x[", 5000), "", ""},
		{"unclosed_subscript_flags", "echo " + strings.Repeat("${x[(", 5000), "", ""},
		{"unclosed_subscript_quotes", "echo " + strings.Repeat("${x['", 5000), "", ""},
		{"many_expansions", strings.Repeat("echo ${x:-y}; ", 5000) + "echo ${(f)x}", hooktest.Deny, labelUnparsed},
	}
}

// 病的に長い入力でも判定と理由を保つ。共有 CI の負荷や race・coverage の計測で変わる実時間は合否に使わない。
func TestPathologicalInput(t *testing.T) {
	for _, tc := range pathologicalInputs() {
		t.Run(tc.name, func(t *testing.T) {
			check(t, tc.want, []hooktest.Case{{Command: tc.command, Label: tc.label}})
		})
	}
}

// 処理時間は通常テストから分け、入力ごとに測定できるようにする。
func BenchmarkPathologicalInput(b *testing.B) {
	for _, tc := range pathologicalInputs() {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.command)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := evaluateAt(tc.command, "/tmp", 0); err != nil {
					b.Fatal(err)
				}
			}
		})
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

// --- shell -c のスクリプト（Claude Code v2.1.296 の Fcn / Lwe / HTr） ---

var (
	labelInlineUnchecked = messages.T(hooktest.Language, idInlineUnchecked)
	labelInlineValue     = commandLabel(idInlineValue)
)

// 調査のきっかけになった実物（rubocop を変更行に絞って流すスクリプト）。パスとハッシュは架空の値に置き換えた。
// sed の `\{0,1\}` が括弧展開に見えて検査できず、`direnv exec "$WT"` の `$WT` がコマンドの位置にあるので ask になる。
const inlineRealWorld = `bash -c '
WT=$PWD; S=/tmp/scratchpad; CHANGED_RB=$(awk "{print \$2}" $S/files.txt | grep -E "\.(rb|schema)$|^Gemfile$"); mb=abc123; CL=$S/changed-lines.txt; : > $CL
for f in $CHANGED_RB; do [ -f "$f" ] || continue; r=$(git diff --unified=0 $mb HEAD -- "$f" | grep "^@@" | sed -n "s/.*+\([0-9]*\),\{0,1\}\([0-9]*\).*/\1 \2/p" | while read a c; do c=${c:-1}; [ "$c" -eq 0 ] && continue; echo "$a-$((a+c-1))"; done | paste -sd "," -); [ -n "$r" ] && echo "$f:$r" >> $CL; done
cat $CL
direnv exec "$WT" env RUBOCOP_LOCAL_CHANGED_LINES="$CL" bundle exec rubocop --cache false --format json $CHANGED_RB > $S/cop.json 2>$S/cop.err; echo rc=$?
jq -r ".summary" $S/cop.json'`

func TestInlineShellUnchecked(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelInlineUnchecked,
		inlineRealWorld,
		// rm の語が無くても止める（一次ゲートを rm 以外でも通す）。
		`bash -c 'sed -n "s/a\{0,1\}/b/p" f; exec "$DIR" ls'`,
		`sh -c 'alias ll=ls; "$RUN" go'`,
		`bash -lc 'echo {x,$y}; $CMD x'`,
		`cd /tmp && bash -c 'IFS=:; $X'`,
		`/bin/bash -c 'echo {x,$y}; sudo $CMD'`,
		`bash-5.2 -c 'echo {x,$y}; $CMD'`,
		`bash --norc -c 'echo {x,$y}; $CMD'`,
		`bash --foo -c 'echo hi; $CMD'`,
		`busybox sh -c 'echo {x,$y}; $CMD'`,
		`zsh -c 'echo ${x:h}; $CMD'`,
		// 位置引数で意味が変わる形（クォートの外の $@ に複数の値）。
		`bash -c 'echo $@; $CMD' _ a b`,
		// wrapper を外してからシェルを探す。
		`timeout 10 env A=1 bash -c 'echo {x,$y}; $CMD'`,
		`sudo -u bob nice -n 5 bash -c 'echo {x,$y}; $CMD'`,
		`A=1 time bash -c 'echo {x,$y}; $CMD'`,
		`echo start; bash -c 'echo {x,$y}; $CMD' 2>/dev/null | tail -1`,
		`if true; then bash -c 'echo {x,$y}; $CMD'; fi`,
		// 2000 文字を超えるスクリプトは検査されない。
		"bash -c 'echo "+strings.Repeat("x", 2000)+"; $CMD'",
		// rm を括弧展開や代入で作る形。
		`bash -c 'echo {x,$y}; /bin/{rm,ls} x'`,
		`bash -c 'echo {x,$y}; env R=/bin/rm x'`,
		// 括弧で区切られた後ろの変数もコマンドの位置とみなす（クォートを無視した読み方）。
		`bash -c 'echo {x,$y} $X'`,
	))
	// 理由文は「包まずに直接実行する」を案内する。
	reason := reasonOf(t, inlineRealWorld, "/tmp")
	if !strings.Contains(reason, messages.T(hooktest.Language, idWhyInlineUnchecked)) ||
		!strings.Contains(reason, messages.Text(hooktest.Language, idHowInlineUnchecked,
			map[string]any{"NoAsk": messages.T(hooktest.Language, idNoAsk)})) {
		t.Errorf("inline unchecked reason: %q", reason)
	}
}

func TestInlineShellAllowed(t *testing.T) {
	check(t, "", hooktest.Commands(
		`bash -c 'echo hi'`,
		`bash -c 'echo $1' _ "$(pwd)"`,
		// 検査できないが、rm になりうる語がコマンドの位置に無い。
		`bash -c 'sed -n "s/a\{0,1\}/b/p" f'`,
		`bash -c 'echo {x,$y} done'`,
		// 検査できるので、組み込みは中身を再帰的に調べて通す。
		`bash -c 'x=1; $X'`,
		`bash -c 'exec "$DIR" ls'`,
		// シェルの -c ではない。
		`bash script.sh`,
		`bash -c`,
		`python -c 'import os; os.system("$X")'`,
		`fish -c 'echo {x,$y}; $CMD'`,
		// xargs などの後ろのスクリプトは見ない（組み込みより狭い側）。
		`xargs bash -c 'echo {x,$y}; $CMD'`,
		// クォートが閉じない入力は判定しない。
		`bash -c 'echo {x,$y}; $CMD`,
		// 案内した書き直し先: 包まずに直接実行する。
		`S=/tmp/scratchpad; for f in $(cat $S/files.txt); do sed -n "s/.*+\([0-9]*\),\{0,1\}.*/\1/p" "$f"; done`,
		`direnv exec "$PWD" env X=1 bundle exec rubocop --format json`,
		// 案内した書き直し先: リテラルのパスを引数に渡す。
		`bash -c 'rm -rf "$1"/build' _ /tmp/work`,
		// heredoc の本文はコマンドではない（スクリプトを書き出すだけ）。
		"cat > /tmp/x.sh <<'EOF'\nbash -c 'echo {x,$y}; $CMD'\nEOF",
		"cat <<-EOF > /tmp/x.sh\n\tbash -c 'echo {x,$y}; $CMD'\nEOF",
	))
	// here-string は heredoc ではないので、同じ行の shell -c は判定する。
	check(t, hooktest.Deny, denyAll(labelInlineUnchecked, `cat <<< x; bash -c 'echo {x,$y}; $CMD'`))
}

func TestInlineShellRemovalTarget(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelInlineValue,
		`bash -c 'rm -rf $1/*' _ "$(pwd)"`,
		`sh -c 'rm -rf "$1"/' _ "$DIR"`,
		`bash -c 'rm -rf $2/*' _ a "$X"`,
	))
	// 置き換え先の名前は見せず、${…} で表す。
	reason := reasonOf(t, `bash -c 'rm -rf $1/*' _ "$(pwd)"`, "/tmp")
	if strings.Contains(reason, inlineShellValue) || !strings.Contains(reason, "${…}/*") {
		t.Errorf("inline value reason must show ${…}: %q", reason)
	}
	// 位置引数を使わない危険な rm は、中身の判定の理由文のまま出す。
	check(t, hooktest.Deny, denyAll(labelEmptyVar, `bash -c 'rm -rf $BASE/*'`, `bash -c 'echo x; rm -rf "$BASE/$name"'`))
	check(t, hooktest.Deny, denyAll(labelCritical, `bash -c 'rm -rf "$1"' _ /`))
	// 外側に cd があると、スクリプトの中の相対パスの基準が分からないので再帰しない。
	check(t, "", hooktest.Commands(`cd sub && bash -c 'rm -rf "$1"' _ .`))
}

func TestInlineShellDisabledByEnvironment(t *testing.T) {
	for _, value := range []string{"1", "true", " YES ", "on"} {
		t.Setenv(disableInlineEnv, value)
		check(t, "", hooktest.Commands(inlineRealWorld, `bash -c 'rm -rf $1/*' _ "$(pwd)"`))
		// rm の分岐は環境変数と関係なく止める。
		check(t, hooktest.Deny, hooktest.Commands(`rm -rf $BASE/*`))
	}
	// 組み込みが真と読まない値では、組み込みも ask を出すので止める。
	for _, value := range []string{"0", "false", "no", ""} {
		t.Setenv(disableInlineEnv, value)
		check(t, hooktest.Deny, denyAll(labelInlineUnchecked, inlineRealWorld))
	}
}

func TestInlineShellGate(t *testing.T) {
	for input, want := range map[string]bool{
		`bash -c 'x'`: true, `sh -ec x`: true, `zsh -lc x`: true, `bash script.sh`: false, `echo -c`: false,
	} {
		if got := gate([]byte(input)); got != want {
			t.Errorf("gate(%q)=%v, want %v", input, got, want)
		}
	}
}

// --- 読み切れない `${…}`（Claude Code v2.1.309 の bKe） ---
//
// 期待値は v2.1.309 の組み込みのパーサーに通した結果（根が ERROR か）に合わせてある。

var labelUnparsed = messages.T(hooktest.Language, idUnparsed)

// 調査のきっかけになった実物（変更された Ruby のファイルを zsh の配列に読み込む）。org・repo・パスは架空の値に置き換えた。
const unparsedRealWorld = `S=/tmp/scratchpad; CHANGED_RB=(${(f)"$(gh api repos/acme/app/pulls/1/files --paginate --jq '.[].filename' | grep -E '\.rb$')"}); bundle exec rubocop $CHANGED_RB`

func TestUnparsedExpansion(t *testing.T) {
	check(t, hooktest.Deny, denyAll(labelUnparsed,
		unparsedRealWorld,
		// zsh のパラメータフラグ・入れ子・添字フラグ。rm の語が無くても止める。
		`echo ${(f)x}`,
		`a=(${(M)F:#*.rb})`,
		`print -l ${(k)m}`,
		`echo ${ (f)x}`,
		`echo ${${x}}`,
		`echo ${$(pwd)}`,
		`echo ${x[(r)a]}`,
		`echo ${x[a]b}`,
		// 展開として読む文脈: ダブルクォート・コマンド置換・バッククォート・here-string・クォートしない heredoc。
		`echo "${(j:,:)a}"`,
		`echo $(echo ${(f)x})`,
		"echo `echo ${(f)x}`",
		`cat <<< "${(f)x}"`,
		"cat <<EOF\n${(f)x}\nEOF",
		// 前に別の展開や cd があっても、後ろにコメントがあっても止める。
		`echo ${y:-x} ${(f)x}`,
		`cd /tmp && echo ${(f)x}`,
		`echo ${(f)x} # tail`,
	))
	// 理由文には読み切れない展開を対応する `}` まで添える。
	reason := reasonOf(t, unparsedRealWorld, "/tmp")
	if !strings.Contains(reason, `(${(f)"$(gh api repos/acme/app/pulls/1/fi…)`) ||
		!strings.Contains(reason, messages.T(hooktest.Language, idWhyUnparsed)) {
		t.Errorf("unparsed reason: %q", reason)
	}
	if reason := reasonOf(t, `echo ${(f)x}; echo done`, "/tmp"); !strings.Contains(reason, "(${(f)x})") {
		t.Errorf("unparsed reason must show the expansion up to its closing brace: %q", reason)
	}
	// rm の分岐が先に当たれば、その理由文を出す（組み込みと同じ順）。
	check(t, hooktest.Deny, denyAll(labelEmptyVar, `rm -rf $D/*; echo ${(f)x}`))
}

func TestUnparsedExpansionAllowed(t *testing.T) {
	check(t, "", hooktest.Commands(
		// bash と zsh の標準の展開は読み切れる。
		`echo ${x}`, `echo ${x:-$(pwd)}`, `echo ${#x}`, `echo ${!x}`, `echo ${x[@]}`, `echo ${#x[@]}`, `echo ${!x[@]}`,
		`echo ${x/a/b}`, `echo ${x%%.*}`, `echo ${x#*/}`, `echo ${x^^}`, `echo ${x,,}`, `echo ${x@Q}`, `echo ${x:0:3}`,
		`echo ${x:t}`, `echo ${~x}`, `echo ${=x}`, `echo ${@}`, `echo ${#}`, `echo ${10}`, `echo ${x[$i]}`, `echo ${x[(r)]}`,
		`echo ${#!}`, `echo ${!#}`,
		// 展開として読まない文脈: シングルクォート・$'…'・コメント・エスケープ・クォートした heredoc。
		`echo '${(f)x}'`, `echo $'${(f)x}'`, `echo x # ${(f)x}`, `# ${(f)x}`, `echo \${(f)x}`,
		"cat <<'EOF'\n${(f)x}\nEOF", "cat <<\\EOF\n${(f)x}\nEOF", "cat <<\"EOF\"\n${(f)x}\nEOF",
		// shell -c のスクリプトの中は、外側のパーサーにはシングルクォートの文字列である（最上位だけが対象）。
		`bash -c 'echo ${(f)x}'`,
		// 区切りの行で閉じない heredoc は、組み込みのパーサーが解析を打ち切るので判定しない。
		"cat <<EOF\nx\nEOF; echo ${(f)x}",
		"cat <<EOF\n${(f)x}",
		// クォートが閉じない入力は判定しない。
		`echo '${(f)x}`,
		// 案内した書き直し先: コマンド置換・パイプ・ファイルからの読み込み・bash の配列。
		`for f in $(cat "$S/files.txt"); do echo "$f"; done`,
		`while read -r f; do echo "$f"; done < "$S/files.txt"`,
		`git diff --name-only | grep -E '\.rb$' | xargs bundle exec rubocop`,
		`FILES=($(git diff --name-only)); echo "${FILES[@]}"`,
	))
}

func TestUnparsedExpansionGate(t *testing.T) {
	for input, want := range map[string]bool{`echo ${x}`: true, `echo $x`: false, `echo {x}`: false} {
		if got := gate([]byte(input)); got != want {
			t.Errorf("gate(%q)=%v, want %v", input, got, want)
		}
	}
}
