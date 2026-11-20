package prbodycapturewarning

import (
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

func replWarning(code string) string {
	if len(code) > maxCodeBytes {
		return ""
	}
	tree, err := js.Parse(parse.NewInputString(code), js.Options{Inline: true})
	if err != nil {
		return ""
	}
	var visitor replVisitor
	js.Walk(&visitor, tree)
	return visitor.warning
}

type replVisitor struct {
	warning string
}

func (v *replVisitor) Enter(node js.INode) js.IVisitor {
	if v.warning != "" {
		return nil
	}
	switch node := node.(type) {
	case *js.CallExpr:
		operand := methodReceiver(node, "text")
		if call, ok := operand.(*js.CallExpr); ok {
			operand = methodReceiver(call, "nothrow")
		}
		if template, ok := operand.(*js.TemplateExpr); ok && isShellTemplate(template) &&
			unsafeCapture(templateShell(template), true) {
			v.warning = idREPLCapture
		}
	case *js.TemplateExpr:
		if isShellTemplate(node) && unsafeCapture(templateShell(node), false) {
			v.warning = idMixedStreams
		}
	}
	return v
}

func (*replVisitor) Exit(js.INode) {}

func methodReceiver(call *js.CallExpr, name string) js.IExpr {
	dot, ok := call.X.(*js.DotExpr)
	if !ok || len(call.Args.List) != 0 || dot.Y.String() != name {
		return nil
	}
	receiver := dot.X
	for {
		switch node := receiver.(type) {
		case *js.GroupExpr:
			receiver = node.X
		case *js.UnaryExpr:
			if node.Op != js.AwaitToken {
				return nil
			}
			receiver = node.X
		default:
			return receiver
		}
	}
}

func isShellTemplate(template *js.TemplateExpr) bool {
	tag, ok := template.Tag.(*js.Var)
	return ok && string(tag.Name()) == "$"
}

// templateShell は補間値を評価せず、その位置を動的な Bash の語として残す。
func templateShell(template *js.TemplateExpr) string {
	var source strings.Builder
	for _, part := range template.List {
		source.Write(part.Value[1 : len(part.Value)-2])
		source.WriteString("${hhx_repl_value}")
	}
	source.Write(template.Tail[1 : len(template.Tail)-1])
	return source.String()
}
