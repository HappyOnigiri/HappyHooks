package generatededitguard

import (
	"reflect"
	"testing"
)

// commandTexts は parseShell の結果を、コマンドごとの語の並びにする（サブシェルの出入りは "(" と ")"）。
func commandTexts(steps []step) [][]string {
	var out [][]string
	for _, current := range steps {
		switch current.kind {
		case stepEnter:
			out = append(out, []string{"("})
		case stepLeave:
			out = append(out, []string{")"})
		case stepCommand:
			var words []string
			for _, w := range current.command.words {
				words = append(words, w.text)
			}
			out = append(out, words)
		}
	}
	return out
}

func TestParseShellWords(t *testing.T) {
	for source, want := range map[string][][]string{
		`a 'b c' "d $x" e\ f`:           {{"a", "b c", "d $x", "e f"}},
		"a; b && c || d | e & f":        {{"a"}, {"b"}, {"c"}, {"d"}, {"e"}, {"f"}},
		"a |& b":                        {{"a"}, {"b"}},
		"(a; b) c":                      {{"("}, {"a"}, {"b"}, {")"}, {"c"}},
		"a $(b $(c)) `d`":               {{"("}, {"("}, {"c"}, {")"}, {"b", "$(c)"}, {")"}, {"("}, {"d"}, {")"}, {"a", "$(b $(c))", "`d`"}},
		"a $((1 + 2))":                  {{"a", "$((1 + 2))"}},
		"a ${x} $1 $@ $":                {{"a", "${x}", "$1", "$@", "$"}},
		"a $'b\\nc'":                    {{"a", "b\nc"}},
		"a # b\nc":                      {{"a"}, {"c"}},
		"a#b":                           {{"a#b"}},
		"a \\\nb":                       {{"a", "b"}},
		`a "b\"c\\d\e"`:                 {{"a", `b"c\d\e`}},
		"a ')' \"(\"":                   {{"a", ")", "("}},
		"a 'unterminated":               {{"a", "unterminated"}},
		"a $(unterminated":              {{"("}, {"unterminated"}, {")"}, {"a", "$(unterminated"}},
		"a ${unterminated":              {{"a", "${unterminated"}},
		"a `unterminated":               {{"a", "`unterminated"}},
		")) a":                          {{"a"}},
		"a \"$(b)\" \"`c`\" \"x\\\ny\"": {{"("}, {"b"}, {")"}, {"("}, {"c"}, {")"}, {"a", "$(b)", "`c`", "xy"}},
	} {
		if got := commandTexts(parseShell(source, 0)); !reflect.DeepEqual(got, want) {
			t.Errorf("parseShell(%q)=%q, want %q", source, got, want)
		}
	}
}

func TestParseShellRedirects(t *testing.T) {
	steps := parseShell("cat <<-EOF > out 2>&1 <in 3>>log &>all\n\tbody\n\tEOF\nnext <<< 'here'", 0)
	if len(steps) != 2 {
		t.Fatalf("steps=%d", len(steps))
	}
	var ops []string
	for _, r := range steps[0].command.redirects {
		ops = append(ops, r.op+" "+r.target.text)
	}
	want := []string{"<<- EOF", "> out", ">& 1", "< in", ">> log", "&> all"}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("redirects=%q, want %q", ops, want)
	}
	if body := steps[0].command.redirects[0].body; body != "body\n" {
		t.Errorf("heredoc body=%q", body)
	}
	if r := steps[1].command.redirects[0]; r.op != "<<<" || r.target.text != "here" {
		t.Errorf("here-string=%+v", r)
	}
	// 区切りの行が無いヒアドキュメントは、残りをすべて本文とする。
	steps = parseShell("python3 <<EOF\nprint(1)", 0)
	if body := steps[0].command.redirects[0].body; body != "print(1)\n" {
		t.Errorf("unterminated heredoc body=%q", body)
	}
	// 入れ子の上限を超えたコマンド置換は追わない。
	if got := parseShell("a $(b)", maxNesting); len(got) != 1 {
		t.Errorf("nested beyond the limit: %d steps", len(got))
	}
}

func TestWordFlags(t *testing.T) {
	w := parseShell("a ~/x *.go '*.go' $x", 0)[0].command.words
	if !w[1].tilde || !w[2].glob || w[3].glob || !w[4].dynamic {
		t.Errorf("flags: %+v", w)
	}
}
