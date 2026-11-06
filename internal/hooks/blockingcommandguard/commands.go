package blockingcommandguard

import (
	"path"
	"strconv"
	"strings"
)

func classifyArgs(args []string, state inputState) string {
	name := path.Base(args[0])
	if strings.Contains(args[0], "/") && args[0] != "/bin/"+name && args[0] != "/usr/bin/"+name {
		return ""
	}
	rest := args[1:]
	if label := foregroundMode(name, rest, state); label != "" {
		return label
	}
	if state.supplied {
		return ""
	}
	if readsStdin(name, rest) {
		return "STDIN"
	}
	return ""
}

func readsStdin(name string, args []string) bool {
	switch name {
	case "cat":
		return fileReader(args, "benstuv", nil, false)
	case "sort":
		return fileReader(args, "bdfinru", nil, false)
	case "wc":
		return fileReader(args, "clmw", nil, false)
	case "head", "tail":
		if !finiteCounts(args) {
			return false
		}
		return fileReader(args, "qv", map[byte]bool{'n': true, 'c': true}, false)
	case "tee":
		return fileReader(args, "ai", nil, true)
	case "grep":
		return scriptedReader(args, "EFGivwxnoclLqshH", false)
	case "sed":
		return scriptedReader(args, "nEr", true)
	case "awk":
		// BEGIN/END や exit により入力前に終了するスクリプトは判定しない。
		return false
	case "python", "python3":
		return interpreterInput(args, "BIu", false)
	case "sh", "bash", "zsh":
		return interpreterInput(args, "s", true)
	case "node", "ruby", "perl":
		return len(args) == 0 || (len(args) == 1 && args[0] == "-")
	case "read":
		return readInput(args)
	default:
		return false
	}
}

// fileReader は既知のオプションだけを消費する。未知のオプションや必要値の欠落は通す。
// always は tee のように、オペランドが入力ファイルではないコマンドにだけ使う。
func fileReader(args []string, flags string, values map[byte]bool, always bool) bool {
	operands, ok := operands(args, flags, values)
	if !ok {
		return false
	}
	if always || len(operands) == 0 {
		return true
	}
	for _, arg := range operands {
		if arg == "-" {
			return true
		}
	}
	return false
}

func operands(args []string, flags string, values map[byte]bool) ([]string, bool) {
	var result []string
	options := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if options && arg == "--" {
			options = false
			continue
		}
		if !options || len(arg) < 2 || arg[0] != '-' {
			result = append(result, arg)
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := arg[j]
			if strings.ContainsRune(flags, rune(flag)) {
				continue
			}
			if !values[flag] {
				return nil, false
			}
			if j == len(arg)-1 {
				i++
				if i == len(args) || args[i] == "" {
					return nil, false
				}
			}
			break
		}
	}
	return result, true
}

// scriptedReader は単純な grep/sed の形だけを見る。-f の外部スクリプトは解析しない。
func scriptedReader(args []string, flags string, sed bool) bool {
	// スクリプトのオプション指定や終了命令の意味は推測せず、単独のリテラルだけを扱う。
	for _, arg := range args {
		if strings.HasPrefix(arg, "-e") || strings.HasPrefix(arg, "-f") {
			return false
		}
	}
	words, ok := operands(args, flags, nil)
	if !ok || len(words) == 0 {
		return false
	}
	// grep の正規表現エラー、sed の q・r などを誤って入力待ちと扱わない。
	// 初期版は grep の固定文字列、sed の単純な表示コマンドだけを対象にする。
	if sed {
		if words[0] != "p" {
			return false
		}
	} else {
		if strings.ContainsAny(words[0], `\[](){}*+?^$|`) || words[0] == "" {
			return false
		}
	}
	return len(words) == 1 || contains(words[1:], "-")
}

func finiteCounts(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "-c") {
			value := arg[2:]
			if value == "" {
				i++
				if i == len(args) {
					return false
				}
				value = args[i]
			}
			number, err := strconv.ParseUint(value, 10, 64)
			if err != nil || number == 0 {
				return false
			}
		}
	}
	return true
}

func interpreterInput(args []string, flags string, shell bool) bool {
	stdinScript := false
	for i, arg := range args {
		if arg == "-" {
			return true
		}
		if arg == "--" {
			return stdinScript || i == len(args)-1
		}
		if !strings.HasPrefix(arg, "-") || len(arg) < 2 {
			return stdinScript
		}
		for _, flag := range arg[1:] {
			if !strings.ContainsRune(flags, flag) {
				return false
			}
		}
		// sh -s の後ろのオペランドはスクリプト名ではなく位置パラメータになる。
		if shell && strings.ContainsRune(arg, 's') {
			stdinScript = true
		}
	}
	return true
}

func readInput(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			// -t は有限、-u は別 FD、-p は zsh では coprocess を指すため、推測しない。
			if arg != "-r" {
				return false
			}
		} else if !variableName(arg) {
			return false
		}
	}
	return true
}

func variableName(value string) bool {
	if value == "" {
		return false
	}
	for i, char := range value {
		letter := char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		digit := i > 0 && char >= '0' && char <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
