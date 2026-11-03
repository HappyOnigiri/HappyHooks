package generatededitguard

import (
	"regexp"
	"slices"
	"strings"
)

// --- その場で渡したコード ---------------------------------------------------------

var (
	pythonRE = regexp.MustCompile(`^(?:python|pypy)[0-9.]*$`)
	rubyRE   = regexp.MustCompile(`^ruby[0-9.]*$`)
	perlRE   = regexp.MustCompile(`^perl[0-9.]*$`)
	shells   = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}
)

// interpreterKind はコマンド名からインタプリタの種類を返す。インタプリタでなければ空文字列を返す。
func interpreterKind(name string) string {
	switch {
	case pythonRE.MatchString(name):
		return "python"
	case name == "node" || name == "nodejs":
		return "node"
	case rubyRE.MatchString(name):
		return "ruby"
	case perlRE.MatchString(name):
		return "perl"
	}
	return ""
}

// interpreterOptions はインタプリタごとの、コードを値に取るオプションと、それ以外の値を取るオプションである。
var interpreterOptions = map[string]struct{ code, valued map[string]bool }{
	"python": {code: map[string]bool{"-c": true}, valued: map[string]bool{"-W": true, "-X": true}},
	"node": {
		code:   map[string]bool{"-e": true, "--eval": true, "-p": true, "--print": true},
		valued: map[string]bool{"-r": true, "--require": true, "--import": true, "--loader": true},
	},
	"ruby": {code: map[string]bool{"-e": true}, valued: map[string]bool{"-r": true, "-I": true, "-C": true, "-E": true}},
	"perl": {code: map[string]bool{"-e": true, "-E": true}, valued: map[string]bool{}},
}

// inlineCode はインタプリタにその場で渡したコードを読み、書き込みの API があれば、コードの中のパスに見える文字列を候補にする。
// スクリプトのファイルやモジュールを実行する形（python3 tools/x.py、python3 -m x）は外部のスクリプトなので見ず、偽を返す。
func (s *bashScan) inlineCode(kind string, c *command, args []word) bool {
	options := interpreterOptions[kind]
	var code []string
	stdin := false
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if options.code[text] || pythonCodeCluster(kind, text) {
			if index+1 < len(args) {
				code = append(code, args[index+1].text)
			}
			index++
			continue
		}
		if value, ok := attachedCode(kind, text); ok {
			code = append(code, value)
			continue
		}
		switch {
		case text == "-":
			stdin = true
		case kind == "python" && text == "-m":
			return false
		case options.valued[text]:
			index++
		case strings.HasPrefix(text, "-") && text != "--":
		default:
			// 最初の引数はスクリプトのファイルである。コードを渡した後なら、スクリプトへの引数である。
			if len(code) == 0 {
				return false
			}
			index = len(args)
		}
	}
	if len(code) == 0 && (stdin || len(operands(args, options.valued)) == 0) {
		code = append(code, s.stdinText(c, false))
	}
	s.codeTargets(strings.Join(code, "\n"))
	return true
}

// pythonCodeCluster は -c で終わる短いオプションのまとまり（-Bc）で、次の引数がコードになる形かを返す。
func pythonCodeCluster(kind, text string) bool {
	return kind == "python" && len(text) > 2 && strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") &&
		strings.HasSuffix(text, "c") && strings.Trim(text[1:len(text)-1], "bBdEhiIOPqsSuv") == ""
}

// attachedCode は -c'...'・--eval=... のように値をくっつけたコードのオプションを読む。
func attachedCode(kind, text string) (string, bool) {
	switch kind {
	case "python":
		// -c の前に値を取らない短いオプションをまとめた形（-Bc）も読む。
		if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
			break
		}
		index := strings.IndexByte(text, 'c')
		if index > 0 && index < len(text)-1 && strings.Trim(text[1:index], "bBdEhiIOPqsSuv") == "" {
			return text[index+1:], true
		}
	case "node":
		for _, prefix := range []string{"--eval=", "--print="} {
			if value, ok := strings.CutPrefix(text, prefix); ok {
				return value, true
			}
		}
	case "ruby", "perl":
		if (strings.HasPrefix(text, "-e") || (kind == "perl" && strings.HasPrefix(text, "-E"))) && len(text) > 2 {
			return text[2:], true
		}
	}
	return "", false
}

var (
	// writeAPIRE はコードの中の書き込みの API である。書き込みモードの open（Python・Ruby・Node の 'w'・'a'・'x'・'+' と、
	// Perl の '>'・'>>'・'+<'）と、ファイルへ書き込む関数を拾う。open の括弧の中は 1 段の入れ子まで見る。
	writeAPIRE = regexp.MustCompile(`open\w*\s*\((?:[^()]|\([^()]*\))*?` +
		`(?:['"][rbtU]*[wax+][rbtU+]*['"]|['"](?:>>?|\+[<>]))` +
		`|\bopen\s*\(?\s*(?:my\s+)?[$\w]+\s*,\s*['"](?:>>?|\+[<>])` +
		`|write_text|write_bytes|\.write\s*\(|writeFile|appendFile|createWriteStream|renameSync|File\.write` +
		`|shutil\.(?:copy\w*|move)|copyFile|os\.(?:rename|replace)`)
	// literalRE はコードの中の文字列リテラル（' " ` で囲んだもの）である。
	literalRE = regexp.MustCompile("'([^'\\\\\n]*)'|\"([^\"\\\\\n]*)\"|`([^`\\\\\n$]*)`")
	// pathLikeRE はパスに見える文字列（空白を含まず、/ か拡張子の . を含む）である。
	pathLikeRE = regexp.MustCompile(`^[^\s]*(?:/|\.[A-Za-z0-9_]+$)[^\s]*$`)
)

// codeTargets はコードに書き込みの API があれば、コードの中のパスに見える文字列リテラルを候補にする。
func (s *bashScan) codeTargets(code string) {
	if !writeAPIRE.MatchString(code) {
		return
	}
	for _, match := range literalRE.FindAllStringSubmatch(code, -1) {
		for _, literal := range match[1:] {
			// Perl の 2 引数の open（">gen.go"）は、モードの記号を除いたものをパスとみなす。
			literal = strings.TrimLeft(literal, "+<> ")
			if literal == "" || len(literal) > 1024 || strings.Contains(literal, "://") || !pathLikeRE.MatchString(literal) {
				continue
			}
			s.add(s.absolute(literal))
		}
	}
}

// shellScript は sh -c '<script>' と、ヒアドキュメントで渡したスクリプトを入れ子のコマンドとして走査する。
func (s *bashScan) shellScript(c *command, args []word, depth int) {
	if depth >= maxNesting {
		return
	}
	script := ""
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if text == "-o" || text == "-O" || text == "+o" || text == "+O" {
			// set -o と shopt の値（pipefail・extglob）を読み飛ばす。
			index++
			continue
		}
		if strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") && strings.Contains(text, "c") {
			if index+1 < len(args) {
				script = args[index+1].text
			}
			break
		}
		if !strings.HasPrefix(text, "-") {
			// スクリプトのファイルを実行する形は外部のスクリプトなので見ない。
			return
		}
	}
	if script == "" {
		script = s.stdinText(c, false)
	}
	if script == "" {
		return
	}
	s.outer = append(s.outer, s.cwd)
	s.walk(parseShell(script, depth+1), depth+1)
	s.cwd = s.outer[len(s.outer)-1]
	s.outer = s.outer[:len(s.outer)-1]
}

// declaration はコマンドが宣言（hhx allow-generated-edit ...）なら、パスを解決して記録する。
// 形の誤った宣言は記録しない（CLI が使い方を示して失敗する）。
func (s *bashScan) declaration(args []word) {
	if len(args) < 2 || args[1].text != DeclareCommand {
		return
	}
	texts := make([]string, 0, len(args)-2)
	for _, arg := range args[2:] {
		texts = append(texts, arg.text)
	}
	reason, indexes, err := parseDeclaration(texts)
	if err != nil {
		return
	}
	var paths, unresolved []string
	for _, index := range indexes {
		arg := args[2+index]
		if arg.dynamic {
			unresolved = append(unresolved, arg.text)
			continue
		}
		for _, path := range s.resolve(arg) {
			if !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) > 0 || len(unresolved) > 0 {
		s.declarations = append(s.declarations, declared{reason: reason, paths: paths, unresolved: unresolved})
	}
}
