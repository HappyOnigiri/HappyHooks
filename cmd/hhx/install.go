package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/install"
	"github.com/HappyOnigiri/hhx/internal/registry"
)

func runInstall(args []string, stdout, stderr io.Writer) int {
	language := displayLanguage()
	agents, ok := parseAgentFlags(language, "install", args, stderr)
	if !ok {
		return 2
	}
	// 実行時の hook は壊れた設定を既定値で読み流すため、壊れていることに気付く機会は install しかない。
	if err := validateConfig(registry.All()); err != nil {
		_, _ = fmt.Fprintf(stderr, "hhx install: %s\n", displayError(language, err))
		return 1
	}
	options, err := installOptions()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hhx install: %v\n", err)
		return 1
	}
	if options.Binary, err = resolveBinary(); err != nil {
		_, _ = fmt.Fprintf(stderr, "hhx install: %s\n", messages.T(language, idNotOnPath))
		return 1
	}
	apply := func(agent hookrt.Agent) (install.Result, error) {
		return install.Install(options, agent, registry.All())
	}
	return applyAgents(language, "install", agents, options, stdout, stderr, apply)
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
	language := displayLanguage()
	agents, ok := parseAgentFlags(language, "uninstall", args, stderr)
	if !ok {
		return 2
	}
	options, err := installOptions()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hhx uninstall: %v\n", err)
		return 1
	}
	apply := func(agent hookrt.Agent) (install.Result, error) { return install.Uninstall(options, agent) }
	return applyAgents(language, "uninstall", agents, options, stdout, stderr, apply)
}

// parseAgentFlags は --agent を読む。省略時は nil を返し、applyAgents が設定ディレクトリのある CLI を選ぶ。
func parseAgentFlags(language i18n.Language, command string, args []string, stderr io.Writer) ([]hookrt.Agent, bool) {
	flags := pflag.NewFlagSet(command, pflag.ContinueOnError)
	flags.SetOutput(stderr)
	names := flags.StringSlice("agent", nil, messages.T(language, idAgentFlag))
	if err := flags.Parse(args); err != nil {
		return nil, false
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, messages.Text(language, idUnexpectedArgument, map[string]any{
			"Command": command, "Argument": strconv.Quote(flags.Arg(0)),
		}))
		return nil, false
	}
	var agents []hookrt.Agent
	for _, name := range *names {
		agent, ok := parseAgent(name)
		if !ok {
			_, _ = fmt.Fprintln(stderr, messages.Text(language, idUnknownAgent, map[string]any{
				"Command": command, "Agent": strconv.Quote(name),
			}))
			return nil, false
		}
		agents = append(agents, agent)
	}
	return agents, true
}

func parseAgent(name string) (hookrt.Agent, bool) {
	for _, agent := range hookrt.Agents() {
		if string(agent) == strings.ToLower(name) {
			return agent, true
		}
	}
	return "", false
}

// applyAgents は agents（省略時は設定ディレクトリのある CLI）へ apply を順に適用する。
// 1 つが失敗しても残りは処理し、終了コードで失敗を伝える。
func applyAgents(
	language i18n.Language,
	command string,
	agents []hookrt.Agent,
	options install.Options,
	stdout, stderr io.Writer,
	apply func(hookrt.Agent) (install.Result, error),
) int {
	if len(agents) == 0 {
		agents = detectAgents(options.Home)
		if len(agents) == 0 {
			_, _ = fmt.Fprintln(stderr, messages.Text(language, idNoAgentDirectory, map[string]any{"Command": command}))
			return 1
		}
	}
	code := 0
	for _, agent := range agents {
		result, err := apply(agent)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "hhx %s: %s: %v\n", command, agent, err)
			code = 1
			continue
		}
		status := messages.T(language, idUnchanged)
		if result.Changed {
			status = messages.T(language, idUpdated)
		}
		_, _ = fmt.Fprintf(stdout, "%s: %s (%s)\n", agent, result.Path, status)
		if result.Changed && result.Resolved != result.Path {
			_, _ = fmt.Fprintln(stdout, messages.Text(language, idWroteSymlinkTarget, map[string]any{"Path": result.Resolved}))
		}
		if result.Backup != "" {
			_, _ = fmt.Fprintln(stdout, messages.Text(language, idBackupSaved, map[string]any{"Path": result.Backup}))
		}
	}
	return code
}

func detectAgents(home string) []hookrt.Agent {
	var agents []hookrt.Agent
	for _, agent := range hookrt.Agents() {
		if info, err := os.Stat(filepath.Join(home, "."+string(agent))); err == nil && info.IsDir() {
			agents = append(agents, agent)
		}
	}
	return agents
}

func installOptions() (install.Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return install.Options{}, err
	}
	return install.Options{
		Home:      home,
		BackupDir: filepath.Join(home, ".local", "state", "hhx", "backups"),
	}, nil
}

// messageError はカタログの文面で表示するエラーである。Error() はログとテストのため英語を返し、
// 表示するときは displayError が表示言語の文面を選ぶ。
type messageError struct {
	id   string
	data map[string]any
}

func (e *messageError) Error() string { return messages.Text(i18n.English, e.id, e.data) }

// displayError は err を表示する文面を返す。messageError なら表示言語の文面にする。
func displayError(language i18n.Language, err error) string {
	var message *messageError
	if errors.As(err, &message) {
		return messages.Text(language, message.id, message.data)
	}
	return err.Error()
}

// validateConfig は設定ファイルの構文、hook 名の綴り、hook 固有の設定の型を definitions に照らして確かめる。
func validateConfig(definitions []hookrt.Definition) error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return validateLoadedConfig(path, cfg, definitions)
}

// validateLoadedConfig は CLI が読み込んだ設定を、再読み込みせずに検査する。
func validateLoadedConfig(path string, cfg *config.Config, definitions []hookrt.Definition) error {
	if _, err := i18n.Parse(cfg.Language); err != nil {
		return &messageError{id: idInvalidLanguage, data: map[string]any{
			"Path": path, "Value": strconv.Quote(cfg.Language),
		}}
	}
	known := map[string]bool{}
	for _, definition := range definitions {
		known[definition.Name] = true
	}
	if unknown := cfg.UnknownHooks(func(name string) bool { return known[name] }); len(unknown) > 0 {
		return &messageError{id: idUnknownHooks, data: map[string]any{
			"Path": path, "Hooks": strings.Join(unknown, ", "),
		}}
	}
	for _, definition := range definitions {
		if definition.NewSettings == nil {
			continue
		}
		if err := cfg.Decode(definition.Name, definition.NewSettings()); err != nil {
			return fmt.Errorf("%s: hooks.%s: %w", path, definition.Name, err)
		}
	}
	return nil
}

// errNotOnPath は PATH に hhx が無いことを表す。表示は idNotOnPath の文面で出す。
var errNotOnPath = errors.New("hhx is not on PATH")

// resolveBinary は登録するコマンド文字列に書く hhx の絶対パスを PATH から決める。
// 実行中のバイナリ（os.Executable）へは退避しない。go run や開発 build の一時的なパスを設定ファイルへ焼き付けないためである。
func resolveBinary() (string, error) {
	binary, err := exec.LookPath("hhx")
	if err != nil {
		return "", errNotOnPath
	}
	return filepath.Abs(binary)
}
