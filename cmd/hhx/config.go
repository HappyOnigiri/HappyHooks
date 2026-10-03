package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/hooks/generatededitguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/prbodystaleness"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/registry"
)

type configValue struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type configHook struct {
	Name     string                 `json:"name"`
	Enabled  bool                   `json:"enabled"`
	Source   string                 `json:"source"`
	Agents   []hookrt.Agent         `json:"agents"`
	Settings map[string]configValue `json:"settings,omitempty"`
}

type configReport struct {
	Path       string       `json:"path"`
	PathSource string       `json:"path_source"`
	Exists     bool         `json:"exists"`
	Language   configValue  `json:"language"`
	Hooks      []configHook `json:"hooks"`
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "show" {
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			_, _ = fmt.Fprint(stdout, messages.T(displayLanguage(), idConfigUsage))
			return 0
		}
		_, _ = fmt.Fprint(stderr, messages.T(displayLanguage(), idConfigUsage))
		return 2
	}
	path, err := config.DefaultPath()
	if err != nil {
		return configFailure(stderr, i18n.English, err)
	}
	cfg, loadErr := config.Load(path)
	language := cfg.DisplayLanguage()
	flags := pflag.NewFlagSet("config show", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, messages.T(language, idConfigJSONFlag))
	help := flags.BoolP("help", "h", false, messages.T(language, idConfigHelpFlag))
	if err := flags.Parse(args[1:]); err != nil {
		_, _ = fmt.Fprintf(stderr, "hhx config show: %v\n", err)
		_, _ = fmt.Fprint(stderr, messages.T(language, idConfigUsage))
		return 2
	}
	if *help {
		_, _ = fmt.Fprint(stdout, messages.T(language, idConfigUsage))
		return 0
	}
	if flags.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, messages.Text(language, idUnexpectedArgument, map[string]any{
			"Command": "config show", "Argument": strconv.Quote(flags.Arg(1)),
		}))
		_, _ = fmt.Fprint(stderr, messages.T(language, idConfigUsage))
		return 2
	}
	if flags.NArg() == 1 && !registry.Known(flags.Arg(0)) {
		_, _ = fmt.Fprintln(stderr, messages.Text(language, idConfigUnknownHook,
			map[string]any{"Hook": strconv.Quote(flags.Arg(0))}))
		_, _ = fmt.Fprint(stderr, messages.T(language, idConfigUsage))
		return 2
	}
	if loadErr != nil {
		return configFailure(stderr, language, loadErr)
	}
	if err := validateLoadedConfig(path, cfg, registry.All()); err != nil {
		return configFailure(stderr, language, err)
	}
	report, err := makeConfigReport(path, cfg, flags.Arg(0))
	if err != nil {
		return configFailure(stderr, language, err)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(report)
	} else {
		err = printConfigReport(stdout, language, report)
	}
	if err != nil {
		return configFailure(stderr, language, err)
	}
	return 0
}

func configFailure(stderr io.Writer, language i18n.Language, err error) int {
	_, _ = fmt.Fprintf(stderr, "hhx config show: %s\n", displayError(language, err))
	return 1
}

func configSource(explicit bool) string {
	if explicit {
		return "config"
	}
	return "default"
}

func makeConfigReport(path string, cfg *config.Config, name string) (configReport, error) {
	report := configReport{
		Path: path, PathSource: "default", Hooks: []configHook{},
		Language: configValue{Value: string(cfg.DisplayLanguage()), Source: configSource(cfg.Language != "")},
	}
	if os.Getenv(config.PathEnv) != "" {
		report.PathSource = config.PathEnv
	}
	if _, err := os.Stat(path); err == nil {
		report.Exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	for _, definition := range registry.All() {
		settings, err := configHookSettings(cfg, definition.Name)
		if err != nil {
			return report, fmt.Errorf("%s: hooks.%s: %w", path, definition.Name, err)
		}
		if name != "" && definition.Name != name {
			continue
		}
		hook := configHook{
			Name: definition.Name, Enabled: cfg.Enabled(definition.Name, definition.DefaultEnabled),
			Source: configSource(cfg.Hooks[definition.Name].Enabled != nil), Agents: []hookrt.Agent{},
		}
		for _, agent := range hookrt.Agents() {
			for _, registration := range definition.Registrations {
				if registration.Agent == agent {
					hook.Agents = append(hook.Agents, agent)
					break
				}
			}
		}
		hook.Settings = settings
		report.Hooks = append(report.Hooks, hook)
	}
	return report, nil
}

func configHookSettings(cfg *config.Config, name string) (map[string]configValue, error) {
	switch name {
	case prbodystaleness.Name:
		var settings prbodystaleness.Settings
		if err := cfg.Decode(name, &settings); err != nil {
			return nil, err
		}
		return map[string]configValue{"update-instruction": {
			Value:  settings.Instruction(cfg.DisplayLanguage()),
			Source: configSource(strings.TrimSpace(settings.UpdateInstruction) != ""),
		}}, nil
	case generatededitguard.Name:
		var settings generatededitguard.Settings
		if err := cfg.Decode(name, &settings); err != nil {
			return nil, err
		}
		markers := []string{}
		for index, expression := range settings.Markers {
			if strings.TrimSpace(expression) == "" {
				continue
			}
			if _, err := regexp.Compile("(?m)" + expression); err != nil {
				return nil, fmt.Errorf("markers[%d]: %w", index, err)
			}
			markers = append(markers, expression)
		}
		return map[string]configValue{"markers": {Value: markers, Source: configSource(settings.Markers != nil)}}, nil
	default:
		return nil, nil
	}
}

func printConfigReport(stdout io.Writer, language i18n.Language, report configReport) error {
	var out strings.Builder
	source := func(value string) string {
		if value == "config" {
			return messages.T(language, idConfigFileSource)
		}
		if value == "default" {
			return messages.T(language, idConfigDefaultSource)
		}
		return value
	}
	status := messages.T(language, idConfigLoaded)
	if !report.Exists {
		status = messages.T(language, idConfigMissing)
	}
	out.WriteString(messages.Text(language, idConfigHeader, map[string]any{
		"Path": report.Path, "PathSource": source(report.PathSource), "Status": status,
		"Language": report.Language.Value, "LanguageSource": source(report.Language.Source),
	}))
	rows := [][]string{strings.Split(messages.T(language, idConfigColumns), "\t")}
	for _, hook := range report.Hooks {
		state := messages.T(language, idConfigDisabled)
		if hook.Enabled {
			state = messages.T(language, idConfigEnabled)
		}
		agents := []string{}
		for _, agent := range hook.Agents {
			label := "Codex"
			if agent == hookrt.Claude {
				label = "Claude Code"
			}
			agents = append(agents, label)
		}
		rows = append(rows, []string{hook.Name, state, source(hook.Source), strings.Join(agents, ", ")})
	}
	printConfigTable(&out, rows)
	out.WriteString(messages.T(language, idConfigScope))
	if err := printConfigSettings(&out, language, report.Hooks, source); err != nil {
		return err
	}
	_, err := io.WriteString(stdout, out.String())
	return err
}

func printConfigSettings(
	out *strings.Builder, language i18n.Language, hooks []configHook, source func(string) string,
) error {
	heading := false
	for _, hook := range hooks {
		if len(hook.Settings) == 0 {
			continue
		}
		if !heading {
			out.WriteString(messages.T(language, idConfigSettings))
			heading = true
		}
		fmt.Fprintf(out, "  %s:\n", hook.Name)
		// 各 hook の項目順を固定し、JSON と同じキーで示す。
		for _, key := range []string{"update-instruction", "markers"} {
			setting, ok := hook.Settings[key]
			if !ok {
				continue
			}
			value, err := json.Marshal(setting.Value)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "    %s: %s (%s)\n", key, value, source(setting.Source))
			if key == "markers" {
				out.WriteString(messages.T(language, idConfigStandardMarker))
			}
		}
	}
	return nil
}

// printConfigTable のセルは登録名・agent 名・日英カタログの文面だけである。
// ASCII は 1 桁、日本語は 2 桁として揃え、全角の見出しでも列がずれないようにする。
func printConfigTable(out *strings.Builder, rows [][]string) {
	width := func(cell string) int {
		size := 0
		for _, char := range cell {
			size++
			if char > 127 {
				size++
			}
		}
		return size
	}
	sizes := make([]int, len(rows[0]))
	for _, row := range rows {
		for index, cell := range row {
			sizes[index] = max(sizes[index], width(cell))
		}
	}
	for _, row := range rows {
		for index, cell := range row {
			out.WriteString(cell)
			if index < len(row)-1 {
				out.WriteString(strings.Repeat(" ", sizes[index]-width(cell)+2))
			}
		}
		out.WriteByte('\n')
	}
}
