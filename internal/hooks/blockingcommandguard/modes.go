package blockingcommandguard

import "strings"

func foregroundMode(name string, args []string, state inputState) string {
	// パイプの下流による終了や、入力から操作を与える用途は推測しない。
	if state.outputPipe {
		return ""
	}
	switch name {
	case "tail":
		if !state.supplied && followsFile(args) {
			return "FOLLOW"
		}
	case "watch":
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			return "FOLLOW"
		}
	case "yes":
		if len(args) == 0 || !strings.HasPrefix(args[0], "-") {
			return "STREAM"
		}
	case "vi", "vim", "nvim", "nano", "emacs":
		if !state.supplied && plainOperands(args) {
			return "EDITOR"
		}
	case "top", "htop":
		if !state.supplied && len(args) == 0 {
			return "INTERACTIVE"
		}
	case "git":
		if !state.supplied && interactiveGit(args) {
			return "INTERACTIVE"
		}
	case "node":
		if !state.supplied && len(args) == 1 && (args[0] == "-i" || args[0] == "--interactive") {
			return "INTERACTIVE"
		}
	case "sh", "bash", "zsh", "python", "python3":
		if !state.supplied && len(args) == 1 && args[0] == "-i" {
			return "INTERACTIVE"
		}
	case "fzf":
		if !state.supplied && len(args) == 0 {
			return "INTERACTIVE"
		}
	}
	return ""
}

func plainOperands(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+") {
			return false
		}
	}
	return true
}

func followsFile(args []string) bool {
	follow := false
	file := false
	for _, arg := range args {
		switch arg {
		case "-f", "-F", "--follow", "--follow=name", "--follow=descriptor":
			follow = true
		default:
			if arg == "" || strings.HasPrefix(arg, "-") {
				return false
			}
			file = true
		}
	}
	return follow && file
}

func interactiveGit(args []string) bool {
	// グローバルオプションの値をサブコマンドやフラグと取り違えない。
	for len(args) > 0 {
		if args[0] == "-C" || args[0] == "-c" {
			if len(args) < 3 {
				return false
			}
			// 設定でエディタやコマンドの意味が変わる場合は通す。
			if args[0] == "-c" {
				return false
			}
			args = args[2:]
			continue
		}
		break
	}
	if len(args) < 2 {
		return false
	}
	var flags []string
	var values, otherFlags []string
	switch args[0] {
	case "add":
		flags = []string{"-p", "--patch", "-i", "--interactive"}
		otherFlags = []string{"-A", "--all", "-u", "--update", "-v", "--verbose"}
	case "rebase":
		flags = []string{"-i", "--interactive"}
		values = []string{"--onto", "--exec", "-x", "--strategy", "-s", "--strategy-option", "-X"}
		otherFlags = []string{"--autosquash", "--autostash", "--rebase-merges", "--root"}
	case "commit":
		flags = []string{"-e", "--edit"}
		values = []string{"-m", "--message", "-F", "--file", "-C", "--reuse-message", "-c", "--reedit-message"}
		otherFlags = []string{"--amend", "-a", "--all", "--allow-empty", "--allow-empty-message", "-v", "--verbose"}
	default:
		return false
	}
	interactive := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if contains(flags, arg) {
			interactive = true
			continue
		}
		if contains(otherFlags, arg) || !strings.HasPrefix(arg, "-") {
			continue
		}
		option, _, attached := strings.Cut(arg, "=")
		if contains(values, option) {
			if !attached {
				i++
				if i == len(args) {
					return false
				}
			}
			continue
		}
		// 未知のフラグや打消し・有限モードは、対話フラグがあっても通す。
		return false
	}
	return interactive
}
