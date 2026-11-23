package dangerousrmguard

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const awkFieldComparison = `\$[0-9]+[ \t]*(?:[<>]=?|==|!=)[ \t]*-?[0-9]+`

var awkComparisonPredicate = regexp.MustCompile(`^[ \t]*` + awkFieldComparison +
	`(?:[ \t]*(?:&&|\|\|)[ \t]*` + awkFieldComparison + `)+[ \t]*$`)

// maskInlineAwkPredicate は、単独の printf | awk でフィールドと整数を比較する式を、クォートを無視する走査から外す。
// この式の && や || の後にある $1 などは awk のフィールドであり、シェルのコマンド名にはならない。
// Claude Code v2.1.330 ではこの形で ask が出なかった。引数の中でコマンドを実行できる形は従来の走査に渡す。
func maskInlineAwkPredicate(script string) string {
	if !strings.Contains(script, "awk") || (!strings.Contains(script, "&&") && !strings.Contains(script, "||")) ||
		jsLength(script) > maxInlineScript {
		return script
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil || len(file.Stmts) != 1 {
		return script
	}
	stmt := file.Stmts[0]
	pipe, ok := stmt.Cmd.(*syntax.BinaryCmd)
	if !ok || pipe.Op != syntax.Pipe || stmt.Background || stmt.Negated || len(stmt.Redirs) != 0 {
		return script
	}
	left, leftOK := pipe.X.Cmd.(*syntax.CallExpr)
	right, rightOK := pipe.Y.Cmd.(*syntax.CallExpr)
	if !leftOK || !rightOK || len(left.Assigns) != 0 || len(right.Assigns) != 0 ||
		len(pipe.X.Redirs) != 0 || len(pipe.Y.Redirs) != 0 ||
		len(left.Args) < 2 || left.Args[0].Lit() != "printf" || !hasInlineAwkSeparator(right.Args) {
		return script
	}
	for _, arg := range left.Args[1:] {
		if !inlineAwkLiteralParts(arg.Parts, false) {
			return script
		}
	}
	predicate := right.Args[len(right.Args)-1]
	if len(predicate.Parts) != 1 {
		return script
	}
	quoted, ok := predicate.Parts[0].(*syntax.SglQuoted)
	if !ok || quoted.Dollar || !awkComparisonPredicate.MatchString(quoted.Value) {
		return script
	}
	return script[:quoted.Pos().Offset()] + "'0'" + script[quoted.End().Offset():]
}

func hasInlineAwkSeparator(args []*syntax.Word) bool {
	if (len(args) != 3 && len(args) != 4) || args[0].Lit() != "awk" || !inlineAwkLiteralParts(args[1].Parts, false) {
		return false
	}
	// 展開のない引数だと確認してからクォートを外し、接続形と分離形を区別する。
	option, err := expand.Literal(nil, args[1])
	if err != nil {
		return false
	}
	if len(args) == 4 {
		return option == "-F" && inlineAwkLiteralParts(args[2].Parts, false)
	}
	return strings.HasPrefix(option, "-F") && option != "-F"
}

func inlineAwkLiteralParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			// Bash のブレース展開や zsh の = 展開など、パーサーが Lit に残す展開も除く。
			if !quoted && strings.ContainsAny(part.Value, "*?[{~=^#") {
				return false
			}
		case *syntax.SglQuoted:
			if part.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if part.Dollar || !inlineAwkLiteralParts(part.Parts, true) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
