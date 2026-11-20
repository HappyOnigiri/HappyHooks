package prbodyeditguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type editRequest struct {
	dir, selector, repo, bodyFile, bodyText, ack string
}

// parseEdit は実行先と本文を静的に特定できる Bash の直接呼び出しを読む。
func parseEdit(command, dir string) (editRequest, bool) {
	if len(command) > 128<<10 || !filepath.IsAbs(dir) {
		return editRequest{}, false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) == 0 {
		return editRequest{}, false
	}
	stmt, next := leadingAnd(file.Stmts[0])
	// Claude Code の Code Mode は作業ディレクトリを cd 'path' && で渡すことがある。
	if next != nil {
		if call, ok := simpleCall(stmt); ok && len(call.Assigns) == 0 &&
			len(call.Args) == 2 && wordText(call.Args[0]) == "cd" {
			target := wordText(call.Args[1])
			if target == "" || strings.HasPrefix(target, "-") {
				return editRequest{}, false
			}
			dir, ok = cdDirectory(dir, target)
			if !ok {
				return editRequest{}, false
			}
			stmt = next
		}
	}
	call, ok := simpleCall(stmt)
	if !ok || len(call.Args) < 4 ||
		wordText(call.Args[0]) != "gh" || wordText(call.Args[1]) != "pr" || wordText(call.Args[2]) != "edit" {
		return editRequest{}, false
	}
	edit := editRequest{dir: dir}
	for _, assign := range call.Assigns {
		// 別の環境変数代入は gh の接続先・認証やファイルの解決を変えうる。
		if assign.Name == nil || assign.Name.Value != ackVariable ||
			assign.Append || assign.Index != nil || assign.Array != nil {
			return editRequest{}, false
		}
		edit.ack = wordText(assign.Value)
	}
	ok = edit.parseArgs(call.Args[3:])
	return edit, ok
}

// leadingAnd は左結合の && の連鎖から、実行順で先頭の二つの文を返す。
func leadingAnd(stmt *syntax.Stmt) (first, next *syntax.Stmt) {
	first = stmt
	for {
		and, ok := first.Cmd.(*syntax.BinaryCmd)
		if !ok || and.Op != syntax.AndStmt {
			return first, next
		}
		first, next = and.X, and.Y
	}
}

func cdDirectory(dir, target string) (string, bool) {
	absolute := filepath.IsAbs(target)
	explicitRelative := target == "." || target == ".." ||
		strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../")
	if !absolute && !explicitRelative {
		// CDPATH によって相対パスの解決が変わりうる。
		return "", false
	}
	components := strings.Split(target, "/")
	if !slices.Contains(components, "..") {
		if absolute {
			return target, true
		}
		return dir + "/" + target, true
	}

	if absolute {
		dir = "/"
	} else {
		// 起点がシンボリックリンクを含む場合、Bash が使う論理 PWD を特定できない。
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || resolved != filepath.Clean(dir) {
			return "", false
		}
	}
	for _, part := range components {
		if part == ".." {
			// Bash の既定の cd は、取り消す成分がディレクトリであることも確かめる。
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return "", false
			}
			dir = filepath.Dir(dir)
			continue
		}
		dir = filepath.Join(dir, part)
	}
	return dir, true
}

func simpleCall(stmt *syntax.Stmt) (*syntax.CallExpr, bool) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	return call, ok && !stmt.Background && !stmt.Negated && len(stmt.Redirs) == 0
}

func (e *editRequest) parseArgs(args []*syntax.Word) bool {
	hasBody, positionalOnly := false, false
	for i := 0; i < len(args); i++ {
		arg, ok := literal(args[i])
		if !ok || arg == "" {
			return false
		}
		if !positionalOnly && arg == "--" {
			positionalOnly = true
			continue
		}
		if positionalOnly || !strings.HasPrefix(arg, "-") {
			if e.selector != "" || strings.HasPrefix(arg, "-") {
				return false
			}
			e.selector = arg
			continue
		}
		flag, value, joined := strings.Cut(arg, "=")
		if !joined && strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "--") && len(flag) > 2 {
			flag, value, joined = flag[:2], flag[2:], true
		}
		if !joined {
			i++
			if i == len(args) {
				return false
			}
			value, ok = literal(args[i])
			if !ok {
				return false
			}
		}
		switch flag {
		case "--body-file", "-F", "--body", "-b":
			if hasBody {
				return false
			}
			hasBody = true
			if flag == "--body-file" || flag == "-F" {
				if value == "" || value == "-" {
					return false
				}
				e.bodyFile = value
			} else {
				e.bodyText = value
			}
		case "--repo", "-R":
			if value == "" || strings.HasPrefix(value, "-") {
				return false
			}
			e.repo = value
		case "--title", "-t", "--base", "-B", "--milestone", "-m",
			"--add-assignee", "--remove-assignee", "--add-label", "--remove-label",
			"--add-project", "--remove-project", "--add-reviewer", "--remove-reviewer":
		default:
			return false
		}
	}
	return hasBody
}

func wordText(word *syntax.Word) string {
	value, _ := literal(word)
	return value
}

// literal は shell の引用だけを解釈し、シェル変数やコマンド置換を評価しない。
func literal(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				if _, ok := inner.(*syntax.Lit); !ok {
					return "", false
				}
			}
		default:
			return "", false
		}
	}
	value, err := expand.Literal(nil, word)
	return value, err == nil
}
