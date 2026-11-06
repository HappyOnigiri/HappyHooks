package blockingcommandguard

import (
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// inputState は明示的な入力元を追跡する。解決できない入力元も supplied として通す。
type inputState struct {
	supplied   bool
	outputPipe bool
}

type scanner struct{}

func classify(command string) string {
	if len(command) > 128<<10 {
		return ""
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}
	s := scanner{}
	unsafe := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.DeclClause:
			unsafe = true
		case *syntax.FuncDecl:
			unsafe = true
			return false
		case *syntax.CallExpr:
			if len(n.Assigns) > 0 {
				unsafe = true
			}
			if len(n.Args) > 0 {
				name, ok := literal(n.Args[0])
				// これらは後続のコマンドの意味や入出力を任意に変えられる。
				if !ok || (name == "exec" && len(n.Args) > 1) {
					unsafe = true
				}
				switch name {
				case "source", ".", "eval", "alias", "trap", "exit", "return", "command", "builtin",
					"set", "shopt", "export", "readonly", "unset", "enable", "declare", "typeset":
					unsafe = true
				}
			}
		}
		return true
	})
	if unsafe {
		return ""
	}
	label, _ := s.statements(file.Stmts, inputState{})
	return label
}

func (s *scanner) statements(stmts []*syntax.Stmt, state inputState) (string, inputState) {
	for _, stmt := range stmts {
		label, next := s.statement(stmt, state)
		if label != "" {
			return label, next
		}
		state = next
	}
	return "", state
}

func redirected(redirs []*syntax.Redirect, state inputState) inputState {
	for _, redir := range redirs {
		if redir.N != nil && redir.N.Value != "0" {
			continue
		}
		if redir.N != nil && redir.N.Value == "0" {
			state.supplied = true
			continue
		}
		switch redir.Op.String() {
		case "<", "<<", "<<-", "<<<", "<&", "<>":
			// FIFO や端末など、入力元自体が待つ可能性は推測しない。
			state.supplied = true
		}
	}
	return state
}

func (s *scanner) statement(stmt *syntax.Stmt, inherited inputState) (string, inputState) {
	if stmt == nil || stmt.Background || stmt.Coprocess {
		return "", inherited
	}
	state := redirected(stmt.Redirs, inherited)
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		label, persistent := s.call(cmd, state)
		if persistent {
			return label, state
		}
		return label, inherited
	case *syntax.Block:
		label, next := s.statements(cmd.Stmts, state)
		// ブロックのリダイレクトは、そのブロックの外には持ち出さない。
		if len(stmt.Redirs) > 0 {
			next = inherited
		}
		return label, next
	case *syntax.Subshell:
		label, _ := s.statements(cmd.Stmts, state)
		return label, inherited
	case *syntax.BinaryCmd:
		return s.binary(cmd, state, inherited)
	case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
		// 実行条件やループによる入力消費は初期版で推測しない。
		return "", inputState{supplied: true, outputPipe: inherited.outputPipe}
	default:
		return "", inherited
	}
}

func (s *scanner) binary(cmd *syntax.BinaryCmd, state, inherited inputState) (string, inputState) {
	if cmd.Op.String() == "|" || cmd.Op.String() == "|&" {
		left := state
		left.outputPipe = true
		if label, _ := s.statement(cmd.X, left); label != "" {
			return label, inherited
		}
		right := state
		right.supplied = true
		label, _ := s.statement(cmd.Y, right)
		return label, inherited
	}
	label, next := s.statement(cmd.X, state)
	if label != "" {
		return label, inherited
	}
	if constantSkipsRight(cmd) {
		return "", next
	}
	label, other := s.statement(cmd.Y, next)
	// && / || のどちらの経路でも入力元が無い場合だけ、後続を入力なしと扱う。
	return label, inputState{supplied: next.supplied || other.supplied, outputPipe: inherited.outputPipe}
}

func constantSkipsRight(cmd *syntax.BinaryCmd) bool {
	call, ok := cmd.X.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 || len(cmd.X.Redirs) > 0 {
		return false
	}
	name, ok := literal(call.Args[0])
	if !ok {
		return false
	}
	if name != "false" && name != "true" && name != ":" {
		return false
	}
	success := name != "false"
	if cmd.X.Negated {
		success = !success
	}
	return (cmd.Op.String() == "&&" && !success) || (cmd.Op.String() == "||" && success)
}

func (s *scanner) call(cmd *syntax.CallExpr, state inputState) (string, bool) {
	if len(cmd.Assigns) > 0 || len(cmd.Args) == 0 {
		return "", false
	}
	args := make([]string, len(cmd.Args))
	for i, word := range cmd.Args {
		value, ok := literal(word)
		if !ok {
			return "", false
		}
		args[i] = value
	}
	if args[0] == "exec" {
		// exec の FD 操作は後続にも残る。コマンドの起動を伴う形は解析しない。
		return "", len(args) == 1
	}
	return classifyArgs(args, state), false
}

// literal は展開を伴わない語だけを返す。expand に任意のコードや環境の解決はさせない。
func literal(word *syntax.Word) (string, bool) {
	if !literalParts(word.Parts, false) {
		return "", false
	}
	value, err := expand.Literal(nil, word)
	return value, err == nil
}

func literalParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if !quoted && (strings.ContainsAny(p.Value, "*?[{") || strings.HasPrefix(p.Value, "~")) {
				return false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if !literalParts(p.Parts, true) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
