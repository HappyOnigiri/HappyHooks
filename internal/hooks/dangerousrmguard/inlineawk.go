package dangerousrmguard

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const awkFieldComparison = `\$[0-9]+[ \t]*(?:[<>]=?|==|!=)[ \t]*-?[0-9]+`

var awkComparisonPredicate = regexp.MustCompile(`^[ \t]*` + awkFieldComparison +
	`(?:[ \t]*(?:&&|\|\|)[ \t]*` + awkFieldComparison + `)+[ \t]*$`)

// maskInlineAwkPredicate は、単独の printf | awk でフィールドと整数を比較する式を、クォートを無視する走査から外す。
// この式の && や || の後にある $1 などは awk のフィールドであり、シェルのコマンド名にはならない。
// Claude Code v2.1.330 ではこの形で ask が出なかった。eval や入れ子のシェルを含む形は従来の走査に渡す。
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
		len(left.Args) < 2 || left.Args[0].Lit() != "printf" || len(right.Args) != 3 || right.Args[0].Lit() != "awk" {
		return script
	}
	separator := right.Args[1].Lit()
	if !strings.HasPrefix(separator, "-F") || len(separator) == len("-F") {
		return script
	}
	predicate := right.Args[2]
	if len(predicate.Parts) != 1 {
		return script
	}
	quoted, ok := predicate.Parts[0].(*syntax.SglQuoted)
	if !ok || quoted.Dollar || !awkComparisonPredicate.MatchString(quoted.Value) {
		return script
	}
	return script[:quoted.Pos().Offset()] + "'0'" + script[quoted.End().Offset():]
}
