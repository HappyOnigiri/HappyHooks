package pushcicontext

import (
	"regexp"
	"strings"
)

var (
	// ヘッダーを狭く限定し、クォート内の << やコマンド置換を本文の始まりと取り違えない。
	documentHeaderRE = regexp.MustCompile(`^cat[ \t]+>[ \t]*[A-Za-z0-9_./${}-]+[ \t]+<<'([A-Za-z_][A-Za-z0-9_]*)'[ \t]*$`)
	documentSetupRE  = regexp.MustCompile(`^[A-Za-z0-9_./${}=; \t-]*$`)
	documentDirRE    = regexp.MustCompile(`^(?:D|S|SP|DIR|OUT)=/[A-Za-z0-9_./-]*$`)
	documentPathRE   = regexp.MustCompile(`^(?:[A-Za-z0-9_./-]|\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})+$`)
)

// onlyWritesDocuments は、引用した本文を保存するだけのコマンド全体を識別する。
// 本文の前後もパス代入・mkdir -p・echo だけに限定する。外部スクリプトの実行・展開・未知の構文は
// 従来の判定へ戻すので、作ったスクリプトを同じ呼び出しで実行する形を見逃さない。
func onlyWritesDocuments(command string) bool {
	lines := strings.Split(command, "\n")
	documents := 0
	for index := 0; index < len(lines); index++ {
		line := strings.Trim(lines[index], " \t\r")
		if match := documentHeaderRE.FindStringSubmatch(line); match != nil {
			index++
			for index < len(lines) && strings.TrimSuffix(lines[index], "\r") != match[1] {
				index++
			}
			if index == len(lines) {
				return false
			}
			documents++
			continue
		}
		if !documentSetupRE.MatchString(line) {
			return false
		}
		for _, segment := range commandSegments(line) {
			if !documentSetup(segment) {
				return false
			}
		}
	}
	return documents > 0
}

func documentSetup(tokens []string) bool {
	if len(tokens) == 1 && documentDirRE.MatchString(tokens[0]) {
		return true
	}
	if tokens[0] == "echo" {
		return !strings.Contains(strings.Join(tokens, " "), "$")
	}
	if len(tokens) < 3 || tokens[0] != "mkdir" || tokens[1] != "-p" {
		return false
	}
	for _, token := range tokens[2:] {
		if !documentPathRE.MatchString(token) {
			return false
		}
	}
	return true
}
