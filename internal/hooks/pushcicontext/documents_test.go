package pushcicontext

import (
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

func TestDocumentExamplesDoNotInject(t *testing.T) {
	commands := []string{
		"cat > /tmp/guide.md <<'EOF'\ngit push origin feature\nEOF",
		"cat > /tmp/guide.md <<'EOF'\ngh pr create --head feature\ngh workflow run ci.yml\nEOF\necho done",
		"D=/tmp/old; mkdir -p $D/release/references $D/unreleased/references\ncat > $D/release/SKILL.md <<'EOF'\ngit push origin feature\nEOF\ncat > $D/unreleased/references/output.md <<'EOF'\ngh pr create\nEOF\necho ok",
		// 引用した本文のコマンド置換は実行されない。
		"cat > /tmp/guide.md <<'EOF'\n$(git push origin feature)\n`gh pr create`\nEOF",
		"cat > /tmp/guide.md <<'EOF'\r\ngit push origin feature\r\nEOF\r\n",
	}
	for _, command := range commands {
		for _, codex := range []bool{false, true} {
			if _, ok := inject(t, command, call{codex: codex}); ok {
				t.Errorf("document command injected (codex=%v): %q", codex, command)
			}
		}
		if out := hooktest.Output(t, Definition(), "", hooktest.Options{Args: []string{command}}); out != "" {
			t.Errorf("document command injected through argv: %q", out)
		}
	}
}

func TestDocumentExceptionKeepsExecutableAndUncertainForms(t *testing.T) {
	const document = "cat > /tmp/guide.sh <<'EOF'\ngit push origin feature\nEOF\n"
	for _, command := range []string{
		document + "git push origin feature",
		"git push origin feature\n" + document,
		document + "bash /tmp/guide.sh",
		document + "bash /tmp/*.sh",
		document + "bash $script",
		document + ". /tmp/guide.sh",
		document + "eval $script",
		document + "python3 tools/run.py",
		"bash <<'EOF'\ngit push origin feature\nEOF",
		"cat > /tmp/guide.md <<EOF\ngit push origin feature\nEOF",
		"cat > /tmp/guide.md <<'EOF'\ngit push origin feature",
		"cat > /tmp/guide.md <<'EOF' && bash /tmp/guide.md\ngit push origin feature\nEOF",
		"cat > /tmp/guide.md <<'EOF'\ngit push origin feature\nEOF\necho $(gh pr create)",
		"PATH=/tmp/bin\n" + document,
		"D=$(git push origin feature)\n" + document,
		"echo \"cat > /tmp/guide.md <<'EOF'\"\ngit push origin feature\nEOF",
	} {
		if onlyWritesDocuments(command) {
			t.Errorf("executable or uncertain command accepted as a document: %q", command)
		}
		if got := triggerKind(command); got != pushOrPR {
			t.Errorf("executable or uncertain command lost its trigger: %q (kind=%v)", command, got)
		}
	}
}

func TestDocumentExceptionRejectsCommandSubstitution(t *testing.T) {
	for _, command := range []string{
		"cat > /tmp/guide.md <<EOF\n$(git push origin feature)\nEOF",
		"cat > $(git push origin feature) <<'EOF'\ntext\nEOF",
	} {
		if onlyWritesDocuments(command) {
			t.Errorf("command substitution accepted as a document: %q", command)
		}
	}
}
