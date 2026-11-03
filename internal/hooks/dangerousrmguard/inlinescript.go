package dangerousrmguard

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// uncheckableScript は `FTr` のうち、スクリプトの形だけで検査を諦める条件である（括弧展開・alias・IFS・zsh 固有の展開）。
func uncheckableScript(script, name string) bool {
	for _, word := range zcnRE.FindAllString(script, -1) {
		if isBraceExpansion(word) {
			return true
		}
	}
	if aliasRE.MatchString(script) || ifsRE.MatchString(ifsReadRE.ReplaceAllLiteralString(script, " read")) {
		return true
	}
	return name == "zsh" && zshRE.MatchString(script)
}

// isBraceExpansion は組み込みの `NTr` である。語が静的に展開できない括弧展開に見えるかを返す。
// sed の `\{0,1\}` のようにクォートの中の括弧も数える（組み込みと同じ）。
func isBraceExpansion(word string) bool {
	text := braceVarRE.ReplaceAllLiteralString(word, "$X")
	opens := strings.Count(text, "{")
	if opens == 0 || !braceSplitRE.MatchString(text) {
		return false
	}
	match := braceShapeRE.FindStringSubmatch(text)
	if opens > 1 || match == nil {
		return true
	}
	head, items, tail := match[1], match[2], match[3]
	if !braceSplitRE.MatchString(items) {
		return false
	}
	if braceHeadRE.MatchString(head) || braceTailRE.MatchString(tail) {
		return true
	}
	for _, item := range braceSplitRE.Split(items, -1) {
		item = quoteCharsRE.ReplaceAllLiteralString(item, "")
		if braceItemRE.MatchString(item) || (item == "" && braceEmptyRE.MatchString(head+tail)) || expandsToRemoval(word) {
			return true
		}
	}
	return false
}

// expandsToRemoval は組み込みの `efn` である。括弧展開の候補のどれかが rm / rmdir になるかを返す。
func expandsToRemoval(word string) bool {
	match := braceShapeRE.FindStringSubmatch(quoteCharsRE.ReplaceAllLiteralString(word, ""))
	if match == nil {
		return false
	}
	for _, item := range braceSplitRE.Split(match[2], -1) {
		if removalPathRE.MatchString(match[1] + item + match[3]) {
			return true
		}
	}
	return false
}

// literalValue は位置引数の値を静的に書ける形にする（組み込みの `Se`）。
// `~` で始まる値は組み込みと同じくホームディレクトリへ展開し、展開できない形（`~user`）は検査を諦める（ok が false）。
// 静的に書けない値は inlineShellValue にする。
func literalValue(arg string) (string, bool) {
	flat := strings.NewReplacer("\t", " ", "\n", " ")
	if tildeRE.MatchString(arg) {
		if expanded := py.Home() + arg[1:]; literalArgRE.MatchString(expanded) {
			return flat.Replace(expanded), true
		}
		return inlineShellValue, true
	}
	if strings.HasPrefix(arg, "~") {
		return "", false
	}
	if literalArgRE.MatchString(arg) {
		return flat.Replace(arg), true
	}
	return inlineShellValue, true
}

// hasUnknownBraceParameter は `/\$\{(?![0-9]+(?:\}|:?\?[^}$`]*\})|[@*#]\})[!#@*0-9]/` である。
// RE2 は先読みを扱えないので、`${` の位置ごとに先読みの部分を先頭一致で確かめる。
func hasUnknownBraceParameter(script string) bool {
	for offset := 0; ; {
		found := strings.Index(script[offset:], "${")
		if found < 0 {
			return false
		}
		rest := script[offset+found+2:]
		if rest != "" && strings.ContainsRune("!#@*0123456789", rune(rest[0])) && !positionalLookRE.MatchString(rest) {
			return true
		}
		offset += found + 2
	}
}

// substitutePositional は位置引数（`$1`・`${1:?}`・`"$@"`・`$#` など）を値に置き換える（`FTr` の末尾）。
// 置き換えると意味が変わる形（クォートの外の `$@` に複数の値、奇数個のクォートの後の `"$@"`）は ok を false にする。
func substitutePositional(script string, values []string) (string, bool) {
	valueAt := func(position string) (string, bool) {
		n, err := strconv.Atoi(position)
		if err != nil || n >= len(values) {
			return "", false
		}
		return values[n], true
	}
	ok := true
	var out strings.Builder
	last := 0
	for _, match := range positionalRE.FindAllStringSubmatchIndex(script, -1) {
		whole := script[match[0]:match[1]]
		group := func(n int) (string, bool) {
			if match[2*n] < 0 {
				return "", false
			}
			return script[match[2*n]:match[2*n+1]], true
		}
		guardedNumber, isGuarded := group(1)
		colon, _ := group(2)
		name, hasName := group(3)
		if !hasName {
			name, hasName = group(4)
		}
		replacement := whole
		switch {
		case strings.HasPrefix(whole, `"`):
			before := escapeRE.ReplaceAllLiteralString(script[:match[0]], "")
			ok = ok && strings.Count(before, `"`)%2 == 0
			quoted := make([]string, 0, len(values))
			for _, value := range values[1:] {
				quoted = append(quoted, `"`+value+`"`)
			}
			replacement = strings.Join(quoted, " ")
		case isGuarded:
			value, exists := valueAt(guardedNumber)
			switch {
			case !exists:
				replacement = inlineShellGuarded
			case (value == "" || value == inlineShellValue) && colon != "":
				replacement = inlineShellGuarded
			default:
				replacement = value
			}
		case !hasName:
			// エスケープはそのまま残す。
		case name == "#":
			replacement = strconv.Itoa(len(values) - 1)
		case name == "@" || name == "*":
			ok = ok && len(values) <= 2
			replacement = ""
			if len(values) > 1 {
				replacement = values[1]
			}
		default:
			replacement, _ = valueAt(name)
		}
		out.WriteString(script[last:match[0]])
		out.WriteString(replacement)
		last = match[1]
	}
	out.WriteString(script[last:])
	return out.String(), ok
}

// isComplexScript は組み込みの `Ncn` である。関数定義・alias・IFS・位置引数への代入・`set` での置換、
// コマンドの位置にある shift・source・eval・trap・変数を含むスクリプトは、置き換えでは意味を保てない。
func isComplexScript(script string) bool {
	if ncnRE.MatchString(script) {
		return true
	}
	for _, words := range splitCommands(script, true) {
		for position, word := range words {
			if !onlyWrappersBefore(words[:position]) {
				break
			}
			if ncnBuiltinRE.MatchString(word) || startsWithUntrackedVariable(word) {
				return true
			}
			if word == "set" && setChangesPositional(words[position+1:]) {
				return true
			}
		}
	}
	return false
}

// onlyWrappersBefore は、語の前が wrapper・オプション・数だけかを返す（`Ncn` の前置きの条件）。
func onlyWrappersBefore(words []string) bool {
	for _, word := range words {
		if !commandWrappers[word] && !ncnDashRE.MatchString(word) {
			return false
		}
	}
	return true
}

// startsWithUntrackedVariable は `/^\$(?!\{?[0-9@*#])/` である。
func startsWithUntrackedVariable(word string) bool {
	if !strings.HasPrefix(word, "$") {
		return false
	}
	rest := strings.TrimPrefix(word[1:], "{")
	return rest == "" || !strings.ContainsRune("0123456789@*#", rune(rest[0]))
}

// setChangesPositional は `set` の引数が位置引数を置き換えるか（`-`・`--`・オプションでない値）を返す。
func setChangesPositional(args []string) bool {
	for position, arg := range args {
		previous := ""
		if position > 0 {
			previous = args[position-1]
		}
		if arg == "-" || arg == "--" || (!ncnFlagRE.MatchString(arg) && !ncnOptionArgRE.MatchString(previous)) {
			return true
		}
	}
	return false
}

// splitCommands は組み込みの `u$` である。スクリプトを区切りで分け、各断片の語（リダイレクトと先頭の代入を除く）を返す。
// quoted が true ならクォートの中の区切りと空白を区切りとして扱わない。false なら区切りとして扱う（組み込みは両方で調べる）。
func splitCommands(script string, quoted bool) [][]string {
	text := script
	if quoted {
		text = usQuotedRE.ReplaceAllStringFunc(text, func(part string) string {
			if strings.HasPrefix(part, `\`) {
				return part
			}
			return usQuotedCharsRE.ReplaceAllLiteralString(part, "\x00")
		})
	}
	text = replaceDuplications(text)
	text = usRedirectRE.ReplaceAllLiteralString(text, ">")
	text = stripQuotes(text)
	var commands [][]string
	for _, part := range usSplitRE.Split(text, -1) {
		var raw []string
		for _, word := range usWordSplitRE.Split(part, -1) {
			if word != "" {
				raw = append(raw, word)
			}
		}
		var kept []string
		leadingAssignments := true
		for position, word := range raw {
			isAssignment := usAssignmentRE.MatchString(word)
			afterRedirect := position > 0 && usRedirectOnlyRE.MatchString(raw[position-1])
			if !usRedirectWordRE.MatchString(word) && !afterRedirect && (!isAssignment || !leadingAssignments) {
				kept = append(kept, word)
			}
			leadingAssignments = leadingAssignments && isAssignment
		}
		commands = append(commands, kept)
	}
	return commands
}

// stripQuotes は `/\$(?=['"])|['"\\]/g` を空にする。先読みの `$` は、続くクォートと一緒に消す形に展開した
// （クォートはどのみち同じ置き換えで消える）。
func stripQuotes(text string) string {
	return usStripQuotesRE.ReplaceAllLiteralString(text, "")
}

// replaceDuplications は `/\d*[<>]&(?:\d+-?|-)(?![^\s;&|()])/g` を空白に置き換える。
// 先読みは一致の直後の 1 文字を確かめる形にした。短い一致へ戻っても直後は数字か `-` になり先読みを満たさないので、
// 最長の一致だけ見ればよい。
func replaceDuplications(text string) string {
	var out strings.Builder
	last := 0
	for _, match := range usDupRE.FindAllStringIndex(text, -1) {
		if match[1] < len(text) {
			next, _ := utf8.DecodeRuneInString(text[match[1]:])
			if !isJSSpace(next) && !strings.ContainsRune(";&|()", next) {
				continue
			}
		}
		out.WriteString(text[last:match[0]])
		out.WriteString(" ")
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// needsInlineCheck は組み込みの `Jcn` である。スクリプトの中身を検査する必要があるか
// （括弧展開・rm の語・区切りの直後の変数のどれかを含むか）を返す。
func needsInlineCheck(script string) bool {
	text := stripQuotes(script)
	return jcnBraceRE.MatchString(text) || jcnRemovalRE.MatchString(text) || jcnVariableRE.MatchString(text)
}

// mayRunRemoval は組み込みの `Dwe` である。スクリプトが rm / rmdir を実行しうるか
// （wrapper やオプションと並んだ rm、コマンドの位置の rm や変数、rm を指す代入や括弧展開）を返す。
// コマンドの位置の判定は、直前の語が wrapper かで見る。`direnv exec "$DIR"` の `exec` も wrapper とみなす（組み込みと同じ）。
func mayRunRemoval(script string) bool {
	isRemoval := func(word string) bool {
		return removalWordRE.MatchString(baseName(redirectTailRE.ReplaceAllLiteralString(word, ""))) ||
			removalListRE.MatchString(word)
	}
	isWrapper := func(word string) bool { return commandWrappers[strings.ToLower(baseName(word))] }
	for _, words := range append(splitCommands(script, true), splitCommands(script, false)...) {
		for position, word := range words {
			if isRemoval(word) {
				for other, neighbor := range words {
					if other != position && (isWrapper(neighbor) ||
						(shortFlagsRE.MatchString(neighbor) && strings.Contains(neighbor, "c")) || findExecRE.MatchString(neighbor)) {
						return true
					}
				}
			}
			if (isRemoval(word) || strings.HasPrefix(word, "$")) && (position == 0 || isWrapper(words[position-1])) {
				return true
			}
			if removalAssignRE.MatchString(word) {
				return true
			}
		}
	}
	for _, word := range zcnRE.FindAllString(script, -1) {
		if expandsToRemoval(word) {
			return true
		}
	}
	return false
}

// countDirectoryChanges はスクリプトの語のうち cd・pushd・popd・chdir の数である。
func countDirectoryChanges(script string) int {
	count := 0
	for _, words := range splitCommands(script, true) {
		for _, word := range words {
			if directoryChangeRE.MatchString(baseName(word)) {
				count++
			}
		}
	}
	return count
}

// inlineCheckDisabled は組み込みの真偽値の環境変数の読み方（`Ne`: 1・true・yes・on を真とする）で disableInlineEnv を読む。
func inlineCheckDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(disableInlineEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// findInlineShell は shell -c の 2 分岐を判定し、拒否するなら発火した分岐を返す。
// 外側の各単純コマンドについて、スクリプトを検査できなければ `Lwe`、検査できれば中身を再帰的に判定する。
func findInlineShell(command, cwd string, depth int) (finding, error) {
	if inlineCheckDisabled() {
		return finding{}, nil
	}
	// 外側の cd の有無は、shell -c が何個あっても 1 回だけ調べる（入力の長さの 2 乗にしない）。
	outerCD, checkedCD := false, false
	for _, argv := range simpleCommands(command) {
		found, ok := findInlineScript(stripWrappers(argv))
		if !ok {
			continue
		}
		judged := found.script
		if found.judgedOK {
			judged = found.judged
		}
		if !needsInlineCheck(judged) && !strings.Contains(found.script, "$'") {
			continue
		}
		unchecked := !found.judgedOK || depth >= maxInlineDepth || jsLength(judged) > maxInlineScript ||
			countDirectoryChanges(judged) > maxInlineDirectoryChanges
		if unchecked {
			if mayRunRemoval(judged) {
				return finding{target: idInlineUnchecked, why: idWhyInlineUnchecked, how: idHowInlineUnchecked}, nil
			}
			continue
		}
		if !checkedCD {
			outerCD, checkedCD = hasDirectoryChange(command), true
		}
		if outerCD {
			continue
		}
		inner := strings.ReplaceAll(judged, inlineShellValue, "${"+inlineShellValue+"}")
		result, err := evaluateAt(inner, cwd, depth+1)
		if err != nil {
			return finding{}, err
		}
		if result.target == "" {
			continue
		}
		if strings.Contains(result.token, "__INLINE_SHELL_") {
			// 置き換え先の名前は書いた覚えのない文字列なので、組み込みの `LKe` と同じく `${…}` に戻して見せる。
			token := strings.NewReplacer(inlineShellGuarded, "${…:?}", "${"+inlineShellValue+"}", "${…}").Replace(result.token)
			result = finding{
				target: idInlineValue, command: result.command, token: token,
				why: idWhyInlineValue, how: idHowInlineValue,
			}
		}
		return result, nil
	}
	return finding{}, nil
}
