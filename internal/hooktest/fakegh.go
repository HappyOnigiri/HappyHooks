package hooktest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 偽の gh は次の規約で動く。gh を呼ぶ hook の Go のテストは、同じ差し替え方を使う。
//
//	FAKE_GH_DIR   … フィクスチャと呼び出しの記録（calls.log）の置き場
//	FAKE_GH_MODE  … ok（既定）/ fail / garbage / empty / hang
//	FAKE_GH_SLEEP … hang のときに眠る秒数（既定 30）
//
// フィクスチャのファイル名:
//
//	gh pr view <n> --repo <owner>/<repo>            → pr_<owner>_<repo>_<n>.json
//	gh api repos/<owner>/<repo>/pulls/comments/<id> → comment_<id>.json
//	gh api graphql ...                              → graphql.json
//
// 呼び出しごとに応答を変えるときは、拡張子の前に回数を挟んだ名前（graphql.2.json など）を置く。
// 回数は同じフィクスチャへの何回目の呼び出しか（1 始まり）で、その名前のファイルが無ければ回数の無い名前を返す。
//
// 実体はテストのバイナリそのものである。FAKE_GH_DIR に gh という名前の symlink を置いて PATH の先頭に足し、
// gh として起動されたテストのバイナリは TestMain の RunFakeGHIfInvoked で偽の gh として振る舞って終わる。
const (
	fakeGHDirEnv   = "FAKE_GH_DIR"
	fakeGHModeEnv  = "FAKE_GH_MODE"
	fakeGHSleepEnv = "FAKE_GH_SLEEP"
	fakeGHCallsLog = "calls.log"
)

// FakeGH は PATH の先頭に置いた偽の gh である。
type FakeGH struct {
	Dir string
}

// FakeGHCall は偽の gh の呼び出し 1 回分である。認証の都合で、どのディレクトリで実行したかも記録する。
type FakeGHCall struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

// NewFakeGH は偽の gh を PATH の先頭に置く。環境変数を t.Setenv で変えるので、並列のテストでは使えない。
// パッケージの TestMain で RunFakeGHIfInvoked を呼んでおく必要がある。
func NewFakeGH(t *testing.T) *FakeGH {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeGHDirEnv, dir)
	t.Setenv(fakeGHModeEnv, "ok")
	// 本物の gh の認証を読ませない（CI のランナーには本物の gh がある）。
	t.Setenv("GH_TOKEN", "")
	return &FakeGH{Dir: dir}
}

// SetMode は偽の gh の応答の形を変える。
func (f *FakeGH) SetMode(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(fakeGHModeEnv, mode)
}

// Write は name のフィクスチャに value の JSON を書く。
func (f *FakeGH) Write(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Calls は記録された呼び出しを順に返す。
func (f *FakeGH) Calls(t *testing.T) []FakeGHCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.Dir, fakeGHCallsLog))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []FakeGHCall
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var call FakeGHCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("calls.log: %v", err)
		}
		calls = append(calls, call)
	}
	return calls
}

// ClearCalls は呼び出しの記録を消す。
func (f *FakeGH) ClearCalls(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.Dir, fakeGHCallsLog)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// RunFakeGHIfInvoked は、テストのバイナリが偽の gh として起動されていれば、gh として振る舞ってプロセスを終える。
// そうでなければ何もしない。gh を呼ぶ hook のテストのパッケージは、TestMain の先頭でこれを呼ぶ。
func RunFakeGHIfInvoked() {
	dir := os.Getenv(fakeGHDirEnv)
	if dir == "" || filepath.Base(os.Args[0]) != "gh" {
		return
	}
	os.Exit(runFakeGH(dir, os.Args[1:]))
}

func runFakeGH(dir string, argv []string) int {
	name := fixtureName(argv)
	// 記録する前に数えるので、今回の呼び出しは含まない。
	nth := countCalls(dir, name) + 1
	// Python の os.getcwd() と同じく、PWD ではなく実体のパスを記録する。
	cwd, _ := syscall.Getwd()
	if line, err := json.Marshal(FakeGHCall{Argv: append([]string{}, argv...), Cwd: cwd}); err == nil {
		flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
		if log, err := os.OpenFile(filepath.Join(dir, fakeGHCallsLog), flags, 0o600); err == nil {
			_, _ = log.Write(append(line, '\n'))
			_ = log.Close()
		}
	}
	switch os.Getenv(fakeGHModeEnv) {
	case "fail":
		_, _ = os.Stderr.WriteString("could not resolve to a PullRequest\n")
		return 1
	case "garbage":
		_, _ = os.Stdout.WriteString("{ this is not json")
		return 0
	case "empty":
		return 0
	case "hang":
		seconds, err := strconv.ParseFloat(os.Getenv(fakeGHSleepEnv), 64)
		if err != nil {
			seconds = 30
		}
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		return 0
	}
	if name == "" {
		_, _ = os.Stderr.WriteString("fake gh: no fixture for " + strings.Join(argv, " ") + "\n")
		return 1
	}
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".json")+"."+strconv.Itoa(nth)+".json"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(dir, name))
	}
	if err != nil {
		_, _ = os.Stderr.WriteString("fake gh: no fixture for " + strings.Join(argv, " ") + "\n")
		return 1
	}
	_, _ = os.Stdout.Write(data)
	return 0
}

// countCalls は calls.log のうち、name のフィクスチャを読む呼び出しの数を返す。
func countCalls(dir, name string) int {
	if name == "" {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(dir, fakeGHCallsLog))
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		var call FakeGHCall
		if json.Unmarshal([]byte(line), &call) == nil && fixtureName(call.Argv) == name {
			count++
		}
	}
	return count
}

func fixtureName(argv []string) string {
	switch {
	case len(argv) >= 3 && argv[0] == "pr" && argv[1] == "view":
		for index, arg := range argv {
			if arg == "--repo" && index+1 < len(argv) {
				return "pr_" + strings.ReplaceAll(argv[index+1], "/", "_") + "_" + argv[2] + ".json"
			}
		}
	case len(argv) >= 2 && argv[0] == "api" && argv[1] == "graphql":
		return "graphql.json"
	case len(argv) >= 2 && argv[0] == "api" && strings.Contains(argv[1], "/pulls/comments/"):
		return "comment_" + argv[1][strings.LastIndex(argv[1], "/")+1:] + ".json"
	}
	return ""
}
