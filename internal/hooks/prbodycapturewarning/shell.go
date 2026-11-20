package prbodycapturewarning

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func unsafeCapture(command string, repl bool) bool {
	if len(command) > maxCodeBytes {
		return false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	unsafe := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if unsafe {
			return false
		}
		switch node := node.(type) {
		case *syntax.Stmt:
			if readsPRBody(node) {
				stdout, merged := outputRouting(node.Redirs)
				unsafe = merged || (repl && !stdout)
			}
		case *syntax.BinaryCmd:
			// |& は、左辺の明示的なリダイレクトの後に 2>&1 を適用する。
			unsafe = node.Op == syntax.PipeAll && readsPRBody(node.X)
		}
		return !unsafe
	})
	return unsafe
}

// outputRouting はリダイレクトを左から評価し、stdout が別の出力先へ移ったか、stdout と stderr が同じ出力先かを返す。
func outputRouting(redirs []*syntax.Redirect) (stdout, merged bool) {
	for _, redir := range redirs {
		switch redir.Op.String() {
		case "&>", "&>>":
			stdout, merged = true, true
		case ">", ">>", ">|", ">&", "<&":
			if redir.Op.String() == "<&" && redir.N == nil {
				continue
			}
			fd := "1"
			if redir.N != nil {
				fd = redir.N.Value
			}
			target := literal(redir.Word)
			dup := redir.Op.String() == ">&" || redir.Op.String() == "<&"
			if redir.N == nil && redir.Op.String() == ">&" && target != "-" && !isDescriptor(target) {
				stdout, merged = true, true
				continue
			}
			switch fd {
			case "1":
				if !dup || target != "1" {
					stdout, merged = true, dup && target == "2"
				}
			case "2":
				if !dup || target != "2" {
					merged = dup && target == "1"
				}
			}
		}
	}
	return stdout, merged
}

func isDescriptor(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}

var pullEndpoint = regexp.MustCompile(`^/?repos/[^/]+/[^/]+/pulls/[0-9]+$`)

func readsPRBody(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) < 3 || path.Base(literal(call.Args[0])) != "gh" {
		return false
	}
	command := literal(call.Args[1])
	if (command != "pr" || literal(call.Args[2]) != "view") && command != "api" {
		return false
	}
	start := 3
	if command == "api" {
		start = 2
	}
	pullAPI := false
	jsonBody, usesJQ, jqBody := false, false, false
	for i := start; i < len(call.Args); i++ {
		flag, value, joined := strings.Cut(literal(call.Args[i]), "=")
		switch flag {
		case "--json", "--jq", "-q", "--method", "-X":
			if !joined && i+1 < len(call.Args) {
				i++
				value = literal(call.Args[i])
			}
			switch flag {
			case "--json":
				jsonBody = slices.Contains(strings.Split(value, ","), "body")
			case "--jq", "-q":
				usesJQ = true
				jqBody = value == ".body" || strings.HasPrefix(value, ".body ")
			default:
				if command == "api" && !strings.EqualFold(value, "GET") {
					return false
				}
			}
		case "--input", "--field", "--raw-field", "-F", "-f":
			if command == "api" {
				return false
			}
		case "--web", "-w", "--comments", "-c":
			if command == "pr" {
				return false
			}
		default:
			if command == "api" {
				pullAPI = pullAPI || pullEndpoint.MatchString(flag)
			}
		}
	}
	if command == "api" {
		return pullAPI && jqBody
	}
	return jsonBody && (!usesJQ || jqBody)
}

// literal はクォートを外す。シェル変数やコマンド置換は評価しない。
func literal(word *syntax.Word) string {
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				if _, ok := inner.(*syntax.Lit); !ok {
					return ""
				}
			}
		default:
			return ""
		}
	}
	value, _ := expand.Literal(nil, word)
	return value
}
