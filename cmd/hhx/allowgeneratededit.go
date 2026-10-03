package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/hooks/generatededitguard"
)

// runAllowGeneratedEdit は、ユーザーが指示した生成ファイルの手編集の宣言を受ける。
// 記録するのは generated-edit-guard（PreToolUse で session_id を受け取れるのは hook だけ）で、CLI は形を確かめて結果を示すだけである。
func runAllowGeneratedEdit(args []string, stdout, stderr io.Writer) int {
	language := displayLanguage()
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = fmt.Fprint(stdout, messages.T(language, idAllowUsage))
		return 0
	}
	declaration, err := generatededitguard.ParseDeclaration(args)
	if err != nil {
		var unknown generatededitguard.UnknownOptionError
		message := messages.T(language, idAllowNoPaths)
		switch {
		case errors.As(err, &unknown):
			message = messages.Text(language, idAllowUnknownOption, map[string]any{"Option": unknown.Option})
		case errors.Is(err, generatededitguard.ErrNoReason):
			message = messages.T(language, idAllowNoReason)
		}
		_, _ = fmt.Fprint(stderr, message+"\n\n"+messages.T(language, idAllowUsage))
		return 2
	}
	if !generatedEditGuardEnabled() {
		_, _ = fmt.Fprint(stdout, messages.T(language, idAllowDisabled))
		return 0
	}
	_, _ = fmt.Fprint(stdout, messages.Text(language, idAllowDeclared, map[string]any{
		"Paths": strings.Join(declaration.Paths, ", "),
	}))
	return 0
}

// generatedEditGuardEnabled は設定で generated-edit-guard が有効かを返す。設定が読めなければ hook と同じく既定（有効）にする。
func generatedEditGuardEnabled() bool {
	path, err := config.DefaultPath()
	if err != nil {
		return true
	}
	cfg, err := config.Load(path)
	if err != nil {
		return true
	}
	definition := generatededitguard.Definition()
	return cfg.Enabled(definition.Name, definition.DefaultEnabled)
}
