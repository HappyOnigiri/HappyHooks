package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/registry"
)

func configFixture(t *testing.T, content string) string {
	t.Helper()
	home, _ := isolate(t)
	path := filepath.Join(home, "settings.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HHX_CONFIG", path)
	return path
}

func configJSON(t *testing.T, hook string) configReport {
	t.Helper()
	args := []string{"config", "show", "--json"}
	if hook != "" {
		args = append(args, hook)
	}
	code, stdout, stderr := runCommand(t, "", args...)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var report configReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestConfigDefaults(t *testing.T) {
	home, _ := isolate(t)
	report := configJSON(t, "")
	if report.Path != filepath.Join(home, ".config", "hhx", "config.yaml") || report.Exists || report.PathSource != "default" {
		t.Fatalf("unexpected path: %+v", report)
	}
	if report.Language != (configValue{Value: "en", Source: "default"}) {
		t.Fatal(report.Language)
	}
	if len(report.Hooks) != len(registry.All()) {
		t.Fatal(report.Hooks)
	}
	for index, definition := range registry.All() {
		hook := report.Hooks[index]
		if hook.Name != definition.Name || hook.Enabled != definition.DefaultEnabled || hook.Source != "default" {
			t.Fatalf("unexpected hook: %+v", hook)
		}
	}
	code, stdout, stderr := runCommand(t, "", "config", "show")
	if code != 0 || stderr != "" || !strings.Contains(stdout, messages.T(i18n.English, idConfigMissing)) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestConfigOverridesAndSettings(t *testing.T) {
	path := configFixture(t, `language: ja
hooks:
  pr-merge-guard:
    enabled: false
  git-hookspath-guard:
    enabled: false
  pr-body-staleness:
    update-instruction: "  Update the body  "
  generated-edit-guard:
    markers: ["^// generated", " "]
`)
	report := configJSON(t, "pr-merge-guard")
	if !report.Exists || report.Path != path || report.PathSource != "HHX_CONFIG" || report.Language.Source != "config" {
		t.Fatal(report)
	}
	if len(report.Hooks) != 1 || report.Hooks[0].Enabled || report.Hooks[0].Source != "config" {
		t.Fatal(report.Hooks)
	}
	if !reflect.DeepEqual(report.Hooks[0].Agents, []hookrt.Agent{hookrt.Claude, hookrt.Codex}) {
		t.Fatal(report.Hooks)
	}
	if configJSON(t, "git-hookspath-guard").Hooks[0].Source != "config" {
		t.Fatal("explicit default lost")
	}
	setting := configJSON(t, "pr-body-staleness").Hooks[0].Settings["update-instruction"]
	if setting.Value != "Update the body。" || setting.Source != "config" {
		t.Fatal(setting)
	}
	markers := configJSON(t, "generated-edit-guard").Hooks[0].Settings["markers"]
	if !reflect.DeepEqual(markers.Value, []any{"^// generated"}) || markers.Source != "config" {
		t.Fatal(markers)
	}
	for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
		// 既定値に依存せず、有効・無効の両方の表示を検証する。
		configFixture(t, "language: "+string(language)+"\nhooks:\n  pr-merge-guard:\n    enabled: false\n")
		code, stdout, stderr := runCommand(t, "", "config", "show")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		for _, id := range []string{idConfigColumns, idConfigEnabled, idConfigDisabled, idConfigScope, idConfigStandardMarker} {
			if !strings.Contains(strings.Join(strings.Fields(stdout), " "), strings.Join(strings.Fields(messages.T(language, id)), " ")) {
				t.Fatalf("missing %s: %s", id, stdout)
			}
		}
	}
}

func TestConfigInvalid(t *testing.T) {
	for _, content := range []string{
		"[", "language: fr", "other: true", "hooks: {unknown: {enabled: true}}",
		"hooks: {pr-merge-guard: {enabled: null}}", "hooks: {pr-body-staleness: {update-instruction: []}}",
		"hooks: {generated-edit-guard: {markers: {bad: true}}}", "hooks: {generated-edit-guard: {markers: ['(']}}",
	} {
		t.Run(content, func(t *testing.T) {
			configFixture(t, content)
			for _, args := range [][]string{{"config", "show"}, {"config", "show", "--json"}} {
				code, stdout, stderr := runCommand(t, "", args...)
				if code != 1 || stdout != "" || stderr == "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
			}
		})
	}
	configFixture(t, "")
	t.Setenv("HHX_CONFIG", t.TempDir())
	if code, stdout, stderr := runCommand(t, "", "config", "show"); code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestConfigArguments(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"config"}, {"config", "other"}, {"config", "show", "unknown"},
		{"config", "show", "pr-merge-guard", "other"}, {"config", "show", "--unknown"},
	} {
		code, stdout, stderr := runCommand(t, "", args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, messages.T(i18n.English, idConfigUsage)) {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	for _, args := range [][]string{{"config", "--help"}, {"config", "show", "-h"}} {
		code, stdout, stderr := runCommand(t, "", args...)
		if code != 0 || stdout == "" || stderr != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}
