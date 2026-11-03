package dangerousrmguard

import (
	"regexp"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- Claude Code v2.1.296 の shell -c スクリプトの検査からの移植 ---------------------
//
// v2.1.296 は `bash -c '<スクリプト>'` のスクリプトを取り出して（`FTr`）削除を検査する（`Fcn`）。
//   - スクリプトを静的に検査できず、かつ実行時に rm になりうる語がコマンドの位置にあると、rm の語が無くても ask にする
//     （`Lwe`。"This command passes a shell -c script that runs rm, and Claude Code could not check the script ..."）。
//   - 検査できるなら、位置引数を実値か `${__INLINE_SHELL_VALUE__}` に置き換えたスクリプトを再帰的に検査する。
//     対象が置き換えた値から作られていれば、理由文を「実行時の値を対象にした shell -c の中の rm」に差し替える（`HTr`）。
//
// どちらも JS の正規表現で書かれているので、JS の意味（`\s` は Unicode の空白、`\w`・`\d`・`\b` は ASCII、
// `.` は改行類を除く 1 文字）で移す。py.Space などの Python の文字クラスは使わない。
//
// 移植していない条件（組み込みでは ask になり得るが、ここでは通す）:
//   - tree-sitter に依存する「検査しきれない」の原因（再帰検査での too-complex、検査の予算切れ、`homeWritten`、
//     スクリプトの中のホームディレクトリのリテラル）と、permission の deny ルールに当たる場合（`Ze`）。
//   - スクリプトの前に置かれる `cd`・環境変数の受け渡し。外側に cd があるときは再帰検査をしない。
//   - コマンドの前置きは代表的なもの（時間・優先度・環境・権限の wrapper）だけを外す。`xargs` などの後ろのスクリプトは見ない。
//
// 組み込みとずれ得る点: 組み込みはこの検査を機能フラグ（`wX()`。既定で有効、サーバーから無効にできる）でも切り替える。
// hook からはフラグが見えないので、無効にされた環境ではこちらだけが止める。環境変数 disableInlineEnv は両方を止める。

const (
	// inlineShellValue は実行時にしか分からない値の置き換え先である（組み込みの `_w`）。
	inlineShellValue = "__INLINE_SHELL_VALUE__"
	// inlineShellGuarded は `${1:?}` の形で守られた空の値の置き換え先である。
	inlineShellGuarded = "${__INLINE_SHELL_GUARDED__:?}"
	// maxInlineScript を超える長さのスクリプトは検査しない（組み込みの `Qcn`。JS の文字列長で数える）。
	maxInlineScript = 2000
	// maxInlineDirectoryChanges を超える数の cd を含むスクリプトは検査しない（組み込みの `LTr`）。
	maxInlineDirectoryChanges = 8
	// maxInlineDepth は入れ子の shell -c を検査する深さの上限である（組み込みの `i3e`）。
	maxInlineDepth = 2
	// disableInlineEnv は組み込みのこの検査を切る環境変数である。設定されていれば組み込みも ask を出さないので、こちらも止めない。
	disableInlineEnv = "CLAUDE_CODE_DISABLE_INLINE_SHELL_RM_PROMPT"

	// jsSpaceChars は JS の `\s` の文字である。
	jsSpaceChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	jsSpace      = `[` + jsSpaceChars + `]`
	jsNotSpace   = `[^` + jsSpaceChars + `]`
	// jsDot は JS の `.`（改行類を除く 1 文字）である。
	jsDot = `[^\n\r\x{2028}\x{2029}]`
	// outerPunctuation は外側のコマンドを単純コマンドに分ける区切りの文字である。
	outerPunctuation = ";&|()<>\n"
)

var (
	// inlineGateRE はシェルの -c らしきオプションである（一次ゲートで使う）。
	inlineGateRE = regexp.MustCompile(`-[A-Za-z]*c`)

	// u$ の部品。
	usQuotedRE       = regexp.MustCompile(`\\(?s:.)|'[^']*'|"(?:[^"\\]|\\` + jsDot + `)*"`)
	usQuotedCharsRE  = regexp.MustCompile(`[` + jsSpaceChars + ";&|()`{}!" + `]`)
	usDupRE          = regexp.MustCompile(`\d*[<>]&(?:\d+-?|-)`)
	usRedirectRE     = regexp.MustCompile(`\d*[<>]&|&>>?|>\|`)
	usStripQuotesRE  = regexp.MustCompile(`\$['"]|['"\\]`)
	usSplitRE        = regexp.MustCompile("[;&|()`{}\\n!]|\\$\\(")
	usWordSplitRE    = regexp.MustCompile(`[ \t]+`)
	usRedirectWordRE = regexp.MustCompile(`^\d*[<>]`)
	usRedirectOnlyRE = regexp.MustCompile(`^\d*[<>]+$`)
	usAssignmentRE   = regexp.MustCompile(`^\w+\+?=`)

	// Dwe の部品。
	removalWordRE   = regexp.MustCompile(`(?i)^(?:\w*=)?rm(?:dir)?$`)
	removalListRE   = regexp.MustCompile(`(?i)(?:^|,)rm(?:dir)?(?:,|\$)`)
	removalAssignRE = regexp.MustCompile(`(?i)^\w+=(?:` + jsNotSpace + `*/)?rm(?:dir)?$`)
	redirectTailRE  = regexp.MustCompile(`(?s)\d*[<>].*$`)
	findExecRE      = regexp.MustCompile(`^-(?:exec(?:dir)?|ok(?:dir)?)$`)
	shortFlagsRE    = regexp.MustCompile(`^-[a-zA-Z]+$`)

	// Zcn・NTr・efn の部品。
	zcnRE         = regexp.MustCompile(`(?:[^` + jsSpaceChars + `'"\\]|\\(?s:.)|'[^']*'|"(?:[^"\\]|\\` + jsDot + `)*")+`)
	braceSplitRE  = regexp.MustCompile(`,|\.\.`)
	braceShapeRE  = regexp.MustCompile(`(?s)^([^{]*)\{([^{}]*)\}(.*)$`)
	braceVarRE    = regexp.MustCompile(`\$\{[^{}]*\}`)
	braceHeadRE   = regexp.MustCompile(`^["']?[/~$]|[{},\\'"]|\.\.`)
	braceTailRE   = regexp.MustCompile(`[~{},\\'"]`)
	braceItemRE   = regexp.MustCompile(`[/~$*?\[]|^\.\.?$|\brm`)
	braceEmptyRE  = regexp.MustCompile(`[$]|^["']?/`)
	quoteCharsRE  = regexp.MustCompile(`["'\\]`)
	removalPathRE = regexp.MustCompile(`(?i)(?:^|/)rm(?:dir)?$`)

	// FTr の部品。
	aliasRE   = regexp.MustCompile(`\balias\b`)
	ifsRE     = regexp.MustCompile(`\bIFS\b`)
	ifsReadRE = regexp.MustCompile(`(?:^|[;&|\n({]` + jsSpace + `*)(?:(?:do|then|else|elif|if|while|until)` + jsSpace +
		`+)?IFS=(?:'[^']*'|"[^"]*"|[^` + jsSpaceChars + `;&|'"\\])*[ \t]+read\b`)
	// 移植元の `=(?!=)\S` は「= の後が = でない空白以外の 1 文字」なので、否定の文字クラスに展開した。
	zshRE = regexp.MustCompile(`(?:^|[` + jsSpaceChars + `;&|(){}])=[^=` + jsSpaceChars +
		`]|\$\{?\w+\}?(?::[htrelquQxsaAcP&]|\[)|\bargv\b|\$(?:[=~^]|\{[=~^(])|\(:`)
	positionalUseRE  = regexp.MustCompile(`\$(?:\{[!#@*0-9]|[#@*0-9])`)
	positionalLookRE = regexp.MustCompile("^(?:[0-9]+(?:\\}|:?\\?[^}$`]*\\})|[@*#]\\})")
	positionalRE     = regexp.MustCompile(`\\(?s:.)|"\$\{?@\}?"|\$\{([0-9]+)(:?)\?[^}]*\}|` +
		`\$(?:\{([0-9]+|[@*#])\}|([0-9@*#]))`)
	escapeRE       = regexp.MustCompile(`\\(?s:.)`)
	literalArgRE   = regexp.MustCompile(`^[\w./@%+=:,*?[\]!^{} \t\n-]*$`)
	tildeRE        = regexp.MustCompile(`^~(?:/|$)`)
	shellVersionRE = regexp.MustCompile(`-\d+(?:\.\d+)*$`)
	shellBashRE    = regexp.MustCompile(`^(?:r?ba)?sh$`)
	shellKshRE     = regexp.MustCompile(`ksh$`)
	shellLongRE    = regexp.MustCompile(`^--?(?:login|posix|noprofile|norc|noediting|verbose|restricted|debugger|` +
		`dump-(?:po-)?strings|wordexp|pretty-print|help|version|(rcfile|init-file))$`)
	shellOptionRE    = regexp.MustCompile(`^[-+].`)
	shellBadFlagsRE  = regexp.MustCompile(`[^abcefhiklmnoprstuvxBCDEHIOPT]`)
	shellKshFlagRE   = regexp.MustCompile(`[oO].`)
	shellOptionArgRE = regexp.MustCompile(`[oO]`)
	shellCommandFlag = regexp.MustCompile(`^-[A-Za-z]*c`)

	// Ncn の部品。
	ncnRE = regexp.MustCompile(`\bfunction\b|\balias\b|\bIFS\b|\w` + jsSpace + `*\(` + jsSpace + `*\)|` +
		`(?:^|[` + jsSpaceChars + `;&|(){}])(?:argv|[0-9]+)(?:\[[^\]]*\])?=|` +
		`(?:^|[` + jsSpaceChars + `;&|(){}!])set` + jsSpace + `[^;&|\n]*(?:\$\(|` + "`)")
	ncnBuiltinRE   = regexp.MustCompile(`^(?:shift|source|\.|eval|trap)$`)
	ncnDashRE      = regexp.MustCompile(`^(?:-|\d+$)`)
	ncnFlagRE      = regexp.MustCompile(`^[-+]`)
	ncnOptionArgRE = regexp.MustCompile(`[oO]$`)

	// Jcn の部品。移植元の `rm(?:dir)?(?![\w.-])` は、直後の 1 文字を消費する形に展開した（パターンの末尾なので意味は同じ）。
	jcnBraceRE    = regexp.MustCompile(`\{[^{}` + jsSpaceChars + `]*(?:,|\.\.)`)
	jcnRemovalRE  = regexp.MustCompile(`(?i)(?:^|[^\w.-])rm(?:dir)?(?:[^\w.-]|$)`)
	jcnVariableRE = regexp.MustCompile("(?:^|[;&|(){}\\n`!])" + jsSpace +
		`*(?:\w+\+?=` + jsNotSpace + `*` + jsSpace + `+)*\$`)

	directoryChangeRE = regexp.MustCompile(`^(?:cd|pushd|popd|chdir)$`)
	nameAssignmentRE  = regexp.MustCompile(`^[A-Za-z_]\w*\+?=`)
	exeSuffixRE       = regexp.MustCompile(`(?i)\.exe$`)
	jsSpaceRE         = regexp.MustCompile(`^` + jsSpace + `$`)

	// shells は組み込みの `rde` である。
	shells = setOf("bash", "sh", "zsh", "dash", "ksh", "mksh", "ash", "hush", "rbash")
	// commandWrappers は組み込みの `B3e` である（後ろに別のコマンドを取る語と、制御構文の予約語）。
	commandWrappers = setOf("sudo", "doas", "exec", "command", "env", "nice", "nohup", "busybox", "time", "timeout",
		"stdbuf", "setsid", "ionice", "pkexec", "runuser", "eval", "xargs",
		"do", "then", "else", "elif", "if", "while", "until", "!", "{", "coproc",
		"builtin", "noglob", "toybox", "trap", "watch", "flock", "chroot", "script")
	// keywords は単純コマンドの先頭から外す予約語である（tree-sitter はこれらを command の外に置く）。
	keywords = setOf("do", "then", "else", "elif", "if", "while", "until", "!", "{", "}", "coproc")
)

func setOf(items ...string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

// inlineScript は `FTr` の結果である。judgedOK が false なら、組み込みはスクリプトを検査できない。
type inlineScript struct {
	script   string
	judged   string
	judgedOK bool
}

// jsLength は JS の文字列長（UTF-16 の符号単位の数）である。
func jsLength(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xFFFF {
			length++
		}
	}
	return length
}

func isJSSpace(r rune) bool {
	return jsSpaceRE.MatchString(string(r))
}

// baseName は組み込みの `TR` である。パスの最後の要素から `.exe` を除く。
func baseName(word string) string {
	if index := strings.LastIndexAny(word, `\/`); index >= 0 {
		word = word[index+1:]
	}
	return exeSuffixRE.ReplaceAllLiteralString(word, "")
}

// shellName は組み込みの `$ke` である。`bash-5.2` のような版の付いた名前も同じシェルとみなす。
func shellName(word string) string {
	return shellVersionRE.ReplaceAllLiteralString(strings.ToLower(baseName(word)), "")
}

// simpleCommands は外側のコマンドを単純コマンドの argv に分ける（組み込みは tree-sitter の command ノードの argv を見る）。
// リダイレクトは演算子と続くファイル名を除く。クォートが閉じないなど字句解析できない入力は判定しない（nil）。
// heredoc を含む入力も判定しない。本文の行をコマンドとして読んでしまい、スクリプトを書き出すだけの
// `cat > x.sh <<'EOF'` まで止めるためである（組み込みは本文をコマンドとして扱わない）。
// コメントやコマンド置換の中までは追わない（組み込みより狭い側）。
func simpleCommands(command string) [][]string {
	tokens, err := py.ShlexSplit(command, " \t\r", outerPunctuation)
	if err != nil {
		return nil
	}
	for _, token := range tokens {
		if strings.Contains(token, "<<") && !strings.Contains(token, "<<<") && strings.Trim(token, outerPunctuation) == "" {
			return nil
		}
	}
	var commands [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			commands = append(commands, current)
		}
		current = nil
	}
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch {
		case token != "" && strings.Trim(token, "<>&") == "" && strings.ContainsAny(token, "<>"):
			index++ // リダイレクトの演算子と、続くファイル名
		case token != "" && strings.Trim(token, outerPunctuation) == "":
			flush()
		default:
			current = append(current, token)
		}
	}
	flush()
	return commands
}

// stripWrappers は単純コマンドの先頭の代入・予約語・wrapper を外す（組み込みの `bL` と `att` の近似）。
// 組み込みが外す wrapper のうち、引数の形が単純な代表的なものだけを扱う。外せない形はそのまま返す（判定しない側）。
func stripWrappers(argv []string) []string {
	for len(argv) > 0 {
		head := argv[0]
		if keywords[head] || nameAssignmentRE.MatchString(head) {
			argv = argv[1:]
			continue
		}
		rest := argv[1:]
		switch baseName(head) {
		case "time", "nohup", "command", "builtin", "noglob", "exec":
			argv = skipOptions(rest, "")
		case "nice":
			argv = skipOptions(rest, "n")
		case "stdbuf":
			argv = skipOptions(rest, "ioe")
		case "sudo":
			argv = skipOptions(rest, "ugCDhprtURT")
		case "doas":
			argv = skipOptions(rest, "uC")
		case "env":
			argv = skipOptions(rest, "uSCP")
			for len(argv) > 0 && nameAssignmentRE.MatchString(argv[0]) {
				argv = argv[1:]
			}
		case "timeout":
			argv = skipOptions(rest, "sk")
			if len(argv) == 0 {
				return argv
			}
			argv = argv[1:] // 時間
		default:
			return argv
		}
	}
	return argv
}

// skipOptions は先頭のオプションを外す。withArgument に含まれる 1 文字のオプションは、続く語を値として取る。
func skipOptions(args []string, withArgument string) []string {
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return args[1:]
		}
		if len(arg) < 2 || arg[0] != '-' {
			return args
		}
		args = args[1:]
		takesValue := !strings.HasPrefix(arg, "--") && len(arg) == 2 && strings.ContainsRune(withArgument, rune(arg[1]))
		if takesValue && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

// findInlineScript は組み込みの `FTr` である。argv がシェルの -c の呼び出しなら、スクリプトと検査できる形を返す。
func findInlineScript(argv []string) (inlineScript, bool) {
	if len(argv) == 0 {
		return inlineScript{}, false
	}
	if len(argv) > 1 && (shellName(argv[0]) == "busybox" || shellName(argv[0]) == "toybox") && shells[shellName(argv[1])] {
		argv = argv[1:]
	}
	name := shellName(argv[0])
	if !shells[name] {
		return inlineScript{}, false
	}
	rest := argv[1:]
	bashLike := shellBashRE.MatchString(name)
	hasCommand, unknown := false, false
	index := 0
	for ; index < len(rest); index++ {
		option := rest[index]
		if option == "-" || option == "--" {
			index++
			break
		}
		if !shellOptionRE.MatchString(option) {
			break
		}
		var long []string
		if bashLike {
			long = shellLongRE.FindStringSubmatch(option)
		}
		switch {
		case long != nil:
			if long[1] != "" {
				index++
			}
		case strings.HasPrefix(option, "--"):
			unknown = true
		default:
			unknown = unknown || (bashLike && shellBadFlagsRE.MatchString(option[1:])) ||
				(shellKshRE.MatchString(name) && shellKshFlagRE.MatchString(option))
			hasCommand = hasCommand || (strings.HasPrefix(option, "-") && strings.Contains(option, "c"))
			index += len(shellOptionArgRE.FindAllString(option, -1))
		}
	}
	if unknown {
		for _, arg := range rest {
			if shellCommandFlag.MatchString(arg) {
				return inlineScript{script: strings.Join(rest, " ")}, true
			}
		}
		return inlineScript{}, false
	}
	if !hasCommand || index >= len(rest) {
		return inlineScript{}, false
	}
	script := rest[index]
	found := inlineScript{script: script}
	if jsLength(script) > maxInlineScript || uncheckableScript(script, name) {
		return found, true
	}
	if !positionalUseRE.MatchString(script) {
		found.judged, found.judgedOK = script, true
		return found, true
	}
	// $0 は -c の後の最初の引数（無ければシェルの名前）、$1 以降はその後の引数である。
	positional := []string{name}
	if index+1 < len(rest) {
		positional = rest[index+1:]
	}
	values := make([]string, len(positional))
	for position, arg := range positional {
		value, ok := literalValue(arg)
		if !ok {
			return found, true
		}
		values[position] = value
	}
	if hasUnknownBraceParameter(script) || strings.Contains(script, "'") || isComplexScript(script) {
		return found, true
	}
	if judged, ok := substitutePositional(script, values); ok && !isComplexScript(judged) {
		found.judged, found.judgedOK = judged, true
	}
	return found, true
}
