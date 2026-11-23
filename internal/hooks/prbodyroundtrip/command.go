package prbodyroundtrip

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type request struct {
	dir, selector, repo, bodyFile string
	create, checkStatus           bool
}

// parseCommand は成功の対象、本文ファイル、作業ディレクトリを静的に特定できる Bash の呼び出しを読む。
func parseCommand(command, dir string) (request, bool) {
	if len(command) > 128<<10 || !filepath.IsAbs(dir) {
		return request{}, false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) == 0 || len(file.Stmts) > 2 {
		return request{}, false
	}
	stmt := file.Stmts[0]
	req := request{dir: dir}
	if len(file.Stmts) == 2 {
		// REPL で末尾の echo が gh の失敗を隠しうるので、出力に rc=0 があるときだけ照合する。
		status := file.Stmts[1]
		if command[status.Pos().Offset():status.End().Offset()] != "echo rc=$?" {
			return request{}, false
		}
		req.checkStatus = true
	}
	if and, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && and.Op == syntax.AndStmt && plain(stmt) && len(stmt.Redirs) == 0 {
		dir, ok := cdTarget(and.X, dir)
		if !ok {
			return request{}, false
		}
		req.dir, stmt = dir, and.Y
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || !plain(stmt) || !outputRedirections(stmt.Redirs) || len(call.Args) < 3 ||
		wordText(call.Args[0]) != "gh" || wordText(call.Args[1]) != "pr" {
		return request{}, false
	}
	switch wordText(call.Args[2]) {
	case "create":
		req.create = true
	case "edit":
	default:
		return request{}, false
	}
	// pr-body-edit-guard の ACK は送信先・本文ファイルの解決を変えない。
	for _, assign := range call.Assigns {
		if req.create || assign.Name == nil || assign.Name.Value != "HHX_PR_BODY_ACK" ||
			assign.Append || assign.Index != nil || assign.Array != nil {
			return request{}, false
		}
		if _, ok := literal(assign.Value); !ok {
			return request{}, false
		}
	}
	ok = req.parseArgs(call.Args[3:])
	return req, ok
}

func plain(stmt *syntax.Stmt) bool {
	return !stmt.Background && !stmt.Negated && !stmt.Coprocess
}

func cdTarget(stmt *syntax.Stmt, dir string) (string, bool) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || !plain(stmt) || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 ||
		len(call.Args) != 2 || wordText(call.Args[0]) != "cd" {
		return "", false
	}
	target := wordText(call.Args[1])
	// 論理 PWD とシンボリックリンクを解決しないと決められないパスを除く。
	if slices.Contains(strings.Split(target, "/"), "..") {
		return "", false
	}
	switch {
	case filepath.IsAbs(target):
		return target, true
	case target == "." || strings.HasPrefix(target, "./"):
		return dir + "/" + target, true
	default:
		// CDPATH によって通常の相対パスの解決は変わりうる。
		return "", false
	}
}

func outputRedirections(redirs []*syntax.Redirect) bool {
	for _, redir := range redirs {
		if redir.Op == syntax.DplOut {
			if redir.N == nil || redir.N.Value != "2" || wordText(redir.Word) != "1" {
				return false
			}
			continue
		}
		if redir.Op != syntax.RdrOut && redir.Op != syntax.AppOut ||
			redir.N != nil && redir.N.Value != "1" && redir.N.Value != "2" || wordText(redir.Word) == "" {
			return false
		}
	}
	return true
}

func (r *request) parseArgs(args []*syntax.Word) bool {
	positionalOnly := false
	for i := 0; i < len(args); i++ {
		arg, ok := literal(args[i])
		if !ok || arg == "" {
			return false
		}
		switch {
		case !positionalOnly && arg == "--":
			positionalOnly = true
			continue
		case positionalOnly || !strings.HasPrefix(arg, "-"):
			if r.create || r.selector != "" || strings.HasPrefix(arg, "-") {
				return false
			}
			r.selector = arg
			continue
		}
		flag, value, joined := splitFlag(arg)
		if r.boolFlag(flag) {
			if joined {
				if _, err := strconv.ParseBool(value); err != nil {
					return false
				}
			}
			continue
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
		case "--body-file", "-F":
			if r.bodyFile != "" || value == "" || value == "-" {
				return false
			}
			r.bodyFile = value
		case "--repo", "-R":
			if r.repo != "" || value == "" || strings.HasPrefix(value, "-") {
				return false
			}
			r.repo = value
		case "--title", "-t", "--base", "-B", "--milestone", "-m":
		default:
			// --body や --attach など本文の出所や送信内容を変える指定を含む場合も対象外。
			if !r.metadataFlag(flag) {
				return false
			}
		}
	}
	return r.bodyFile != ""
}

func splitFlag(arg string) (flag, value string, joined bool) {
	if len(arg) > 2 && !strings.HasPrefix(arg, "--") {
		return arg[:2], strings.TrimPrefix(arg[2:], "="), true
	}
	return strings.Cut(arg, "=")
}

func (r request) boolFlag(flag string) bool {
	if r.create {
		switch flag {
		case "--draft", "-d", "--no-maintainer-edit", "--fill", "-f", "--fill-first", "--fill-verbose":
			return true
		}
	}
	return !r.create && flag == "--remove-milestone"
}

func (r request) metadataFlag(flag string) bool {
	if r.create {
		switch flag {
		case "--head", "-H", "--reviewer", "-r", "--assignee", "-a", "--label", "-l", "--project", "-p":
			return true
		}
		return false
	}
	switch flag {
	case "--add-assignee", "--remove-assignee", "--add-label", "--remove-label",
		"--add-project", "--remove-project", "--add-reviewer", "--remove-reviewer":
		return true
	}
	return false
}

func wordText(word *syntax.Word) string {
	value, _ := literal(word)
	return value
}

// literal は引用を解釈し、シェル変数・コマンド置換・パス名展開を含む語を除く。
func literal(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	for i, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, "*?[{}") || i == 0 && strings.HasPrefix(part.Value, "~") {
				return "", false
			}
		case *syntax.SglQuoted:
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
