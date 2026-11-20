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
	paths := []struct {
		node  syntax.Node
		route outputRoute
		merge bool
	}{{route: terminalRoute}}
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			paths = paths[:len(paths)-1]
			return true
		}
		if unsafe {
			return false
		}
		parent := paths[len(paths)-1]
		current := parent
		current.node = node
		switch node := node.(type) {
		case *syntax.Stmt:
			current.merge = false
			if pipe, ok := parent.node.(*syntax.BinaryCmd); ok && isPipe(pipe.Op) {
				if node == pipe.X {
					dest := current.route.newDestination("")
					dest.replCapture = current.route.stdout.replCapture
					current.route.stdout = dest
					current.merge = pipe.Op == syntax.PipeAll
				} else {
					// 左結合のパイプの結果を次のパイプに渡すのは、その最終段である。
					current.merge = parent.merge
				}
			}
			switch node.Cmd.(type) {
			case *syntax.Block, *syntax.Subshell:
				current.route = outputRouting(current.route, node.Redirs, current.merge)
			default:
				if readsPRBody(node) {
					result := outputRouting(current.route, node.Redirs, current.merge)
					unsafe = result.merged() || (repl && result.stdout.replCapture)
				}
			}
		case *syntax.CmdSubst:
			// コマンド置換の stdout は外側のリダイレクト先ではなく変数などに取り込まれる。
			current.route = terminalRoute
		}
		if unsafe {
			return false
		}
		paths = append(paths, current)
		return true
	})
	return unsafe
}

func isPipe(op syntax.BinCmdOperator) bool {
	return op == syntax.Pipe || op == syntax.PipeAll
}

type outputRoute struct {
	stdout, stderr destination
	nextID         int
}

type destination struct {
	file        string
	id          int
	closed      bool
	replCapture bool
}

var terminalRoute = outputRoute{
	stdout: destination{id: 1, replCapture: true}, stderr: destination{id: 2}, nextID: 2,
}

func (r outputRoute) merged() bool {
	return !r.stdout.closed && r.stdout == r.stderr
}

func (r *outputRoute) newDestination(file string) destination {
	if file != "" {
		return destination{file: file}
	}
	r.nextID++
	return destination{id: r.nextID}
}

// outputRouting は継承した出力先に、リダイレクトを左から適用する。
func outputRouting(route outputRoute, redirs []*syntax.Redirect, merge bool) outputRoute {
	for _, redir := range redirs {
		op := redir.Op.String()
		switch op {
		case "&>", "&>>":
			route.stdout = route.newDestination(literal(redir.Word))
			route.stderr = route.stdout
		case ">", ">>", ">|", ">&", "<&":
			route.redirect(redir, op)
		}
	}
	// |& は左辺の明示的なリダイレクトを適用した後で stderr を stdout に合流させる。
	if merge {
		route.stderr = route.stdout
	}
	return route
}

func (r *outputRoute) redirect(redir *syntax.Redirect, op string) {
	fd := "1"
	if redir.N != nil {
		fd = redir.N.Value
	} else if op == "<&" {
		return
	}
	if fd != "1" && fd != "2" {
		return
	}
	target := literal(redir.Word)
	if redir.N == nil && op == ">&" && target != "-" && !isDescriptor(target) {
		r.stdout = r.newDestination(target)
		r.stderr = r.stdout
		return
	}
	var dest destination
	if op == ">&" || op == "<&" {
		switch target {
		case "1":
			dest = r.stdout
		case "2":
			dest = r.stderr
		case "-":
			dest.closed = true
		default:
			dest = r.newDestination("")
		}
	} else {
		dest = r.newDestination(target)
	}
	if fd == "1" {
		r.stdout = dest
	} else {
		r.stderr = dest
	}
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
