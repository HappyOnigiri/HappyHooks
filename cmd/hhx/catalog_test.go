package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/waitci"
)

// testLanguage はテストを流す表示言語である（hook のテストと同じく HHX_TEST_LANGUAGE で選ぶ）。
var testLanguage = hooktest.Language

// TestMain は開発者の設定ファイルを読ませない。表示言語だけをテストの言語にした設定を渡す。
// install のテストは isolate で一時的な HOME に移り、設定ファイルの無い状態（英語）で流す。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hhx-cmd-test-")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("language: "+string(testLanguage)+"\n"), 0o600); err != nil {
		panic(err)
	}
	_ = os.Setenv(config.PathEnv, path)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func noPRMessage(sha string) string {
	return i18n.Text(testLanguage, "wait-ci.no-pr", map[string]any{"SHA": sha})
}

// cmd/hhx は全 hook と wait-ci を取り込むので、ここで全カタログが登録されている。
func TestCatalogIsValid(t *testing.T) {
	if err := i18n.Validate(); err != nil {
		t.Fatal(err)
	}
	all, err := i18n.All()
	if err != nil {
		t.Fatal(err)
	}
	// 全 hook（13 本）・wait-ci・CLI の表が登録されていること。
	prefixes := map[string]bool{}
	for id := range all {
		prefix, _, _ := strings.Cut(id, ".")
		prefixes[prefix] = true
	}
	for _, name := range []string{
		"pr-merge-guard", "idle-wait-guard", "forbidden-term-guard", "git-hookspath-guard", "irreversible-guard",
		"dangerous-rm-guard", "discard-guard", "exit-plan-subagent-guard", "push-ci-context", "pr-body-staleness",
		"pr-context", "agents-local-context", "generated-edit-guard", "wait-ci", "cli",
	} {
		if !prefixes[name] {
			t.Errorf("no messages are registered for %s", name)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// goFiles は root 以下の Go のソースを返す。
func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "bin", "artifacts", "tmp", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// stringLiterals は file の文字列リテラルを、値と行番号の組で返す。
func stringLiterals(t *testing.T, fileSet *token.FileSet, file string) map[int][]string {
	t.Helper()
	parsed, err := parser.ParseFile(fileSet, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	literals := map[int][]string{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("%s: %v", fileSet.Position(literal.Pos()), err)
		}
		line := fileSet.Position(literal.Pos()).Line
		literals[line] = append(literals[line], value)
		return true
	})
	return literals
}

func containsJapanese(text string) bool {
	for _, char := range text {
		if unicode.In(char, unicode.Hiragana, unicode.Katakana, unicode.Han) ||
			strings.ContainsRune("。、「」『』（）・ー", char) {
			return true
		}
	}
	return false
}

// 文面はカタログ（messages.go）から出す。それ以外の Go のソース（テストを除く）に日本語の文字列を置かない。移し漏れを防ぐ。
func TestNoJapaneseStringsOutsideCatalogs(t *testing.T) {
	root := repositoryRoot(t)
	fileSet := token.NewFileSet()
	for _, file := range goFiles(t, root) {
		if strings.HasSuffix(file, "_test.go") || filepath.Base(file) == "messages.go" {
			continue
		}
		for line, values := range stringLiterals(t, fileSet, file) {
			for _, value := range values {
				if containsJapanese(value) {
					relative, _ := filepath.Rel(root, file)
					t.Errorf("%s:%d: a Japanese string outside a catalog: %q", relative, line, value)
				}
			}
		}
	}
}

var messageIDPattern = regexp.MustCompile(`^[a-z0-9-]+(?:\.[a-z0-9-]+)+$`)

// コードとテストが参照する ID（カタログの接頭辞で始まる ID の形の文字列）が、すべて表にあること。
// 未知の ID は ID そのものが表示されるので、綴りの誤りを実行時まで持ち越さない。
func TestReferencedMessageIDsExist(t *testing.T) {
	all, err := i18n.All()
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[string]bool{}
	for id := range all {
		prefix, _, _ := strings.Cut(id, ".")
		prefixes[prefix] = true
	}
	root := repositoryRoot(t)
	fileSet := token.NewFileSet()
	for _, file := range goFiles(t, root) {
		for line, values := range stringLiterals(t, fileSet, file) {
			for _, value := range values {
				prefix, _, _ := strings.Cut(value, ".")
				if !messageIDPattern.MatchString(value) || !prefixes[prefix] {
					continue
				}
				if _, ok := all[value]; !ok {
					relative, _ := filepath.Rel(root, file)
					t.Errorf("%s:%d: unknown message ID %q", relative, line, value)
				}
			}
		}
	}
}

// 設定の language は、未指定・en・ja・不正な値で、英・英・日・英になる。不正な値は install が報告する。
func TestLanguageSettingSelectsTheOutput(t *testing.T) {
	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr merge 1"},"cwd":"/tmp"}`
	for _, testCase := range []struct {
		config string
		want   i18n.Language
	}{
		{"", i18n.English},
		{"hooks: {}\n", i18n.English},
		{"language: en\n", i18n.English},
		{"language: ja\n", i18n.Japanese},
		{"language: fr\n", i18n.English},
	} {
		home, _ := isolate(t)
		path := filepath.Join(home, "config.yaml")
		if testCase.config != "" {
			if err := os.WriteFile(path, []byte(testCase.config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv(config.PathEnv, path)
		_, stdout, _ := runCommand(t, payload, "hook", "pr-merge-guard")
		if !strings.Contains(stdout, i18n.Text(testCase.want, "pr-merge-guard.how", nil)) {
			t.Errorf("config %q: the reason is not in %s: %s", testCase.config, testCase.want, stdout)
		}
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runCommand(t, "", "install", "--agent", "claude")
		invalid := strings.Contains(stderr, "language")
		if testCase.config == "language: fr\n" {
			if code != 1 || !invalid || !strings.Contains(stderr, `"fr"`) {
				t.Errorf("an invalid language must be reported: code=%d stderr=%q", code, stderr)
			}
		} else if code != 0 || invalid {
			t.Errorf("config %q: code=%d stderr=%q", testCase.config, code, stderr)
		}
	}
}

// install の設定検査のエラーは、Error() が表示言語によらず英語で、表示するときだけ表示言語の文面になる。
func TestMessageErrorIsEnglishAndDisplayedInTheLanguage(t *testing.T) {
	err := error(&messageError{id: idInvalidLanguage, data: map[string]any{"Path": "/p/config.yaml", "Value": `"fr"`}})
	if want := messages.Text(i18n.English, idInvalidLanguage, map[string]any{"Path": "/p/config.yaml", "Value": `"fr"`}); err.Error() != want {
		t.Errorf("Error()=%q, want %q", err.Error(), want)
	}
	for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
		want := messages.Text(language, idInvalidLanguage, map[string]any{"Path": "/p/config.yaml", "Value": `"fr"`})
		if got := displayError(language, err); got != want {
			t.Errorf("%s: displayError()=%q, want %q", language, got, want)
		}
	}
}

// 最終行 `wait-ci: exit=...`・接頭辞・終了コードは読み手との契約なので、表示言語で変わらない。
func TestWaitCIContractIsTheSameInEveryLanguage(t *testing.T) {
	check := waitci.Check{Name: "tests", Done: true, Result: "FAILURE", URL: "https://example.test/tests"}
	outcomes := []waitci.Outcome{
		{Status: waitci.StatusNoPR}, {Status: waitci.StatusError, Message: "boom"},
		{Status: waitci.StatusHeadTimeout, Message: "cafe", Elapsed: 5}, {Status: waitci.StatusConflict},
		{Status: waitci.StatusNoCI, Elapsed: 5}, {Status: waitci.StatusEmptyTimeout, Elapsed: 5},
		{Status: waitci.StatusComplete, Head: "abc", Checks: []waitci.Check{check}, Conflicting: true},
		{Status: waitci.StatusTimeout, Head: "abc", Checks: []waitci.Check{{Name: "build"}}},
	}
	for _, outcome := range outcomes {
		results := map[i18n.Language][]string{}
		codes := map[i18n.Language]int{}
		for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
			var lines []string
			say := func(line string) { lines = append(lines, line) }
			finish := func(code, failed, total int) int {
				say("wait-ci: exit=" + strconv.Itoa(code) + " failed=" + strconv.Itoa(failed) + " total=" + strconv.Itoa(total))
				return code
			}
			codes[language] = reportWaitCI(language, outcome, false, false, say, finish)
			results[language] = lines
		}
		english, japanese := results[i18n.English], results[i18n.Japanese]
		if codes[i18n.English] != codes[i18n.Japanese] || len(english) != len(japanese) ||
			english[len(english)-1] != japanese[len(japanese)-1] {
			t.Errorf("%v: en=%q (%d) ja=%q (%d)", outcome.Status, english, codes[i18n.English], japanese, codes[i18n.Japanese])
			continue
		}
		for index := range english {
			// 結論・警告の行は `wait-ci: ` で始まり、明細の行（PR head: と check の一覧）は言語で変わらない。
			if strings.HasPrefix(english[index], "wait-ci: ") != strings.HasPrefix(japanese[index], "wait-ci: ") ||
				(!strings.HasPrefix(english[index], "wait-ci: ") && english[index] != japanese[index]) {
				t.Errorf("%v: line %d differs in shape: %q / %q", outcome.Status, index, english[index], japanese[index])
			}
		}
	}
}

// HHX_CATALOG_DUMP にパスを渡すと、全 ID の日英対訳を Markdown で書き出す。訳の見直し用で、リポジトリには置かない。
func TestDumpCatalogForReview(t *testing.T) {
	path := os.Getenv("HHX_CATALOG_DUMP")
	if path == "" {
		t.Skip("set HHX_CATALOG_DUMP to write the catalog")
	}
	all, err := i18n.All()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out strings.Builder
	for _, id := range ids {
		out.WriteString("## " + id + "\n\n### en\n\n```text\n" + all[id].EN + "\n```\n\n### ja\n\n```text\n" + all[id].JA + "\n```\n\n")
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
