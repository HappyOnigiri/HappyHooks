package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applierFor は install.sh を配る偽のサーバーと、実行を差し替えた Applier を作る。
func applierFor(t *testing.T, body string, run func(scriptPath string, output io.Writer) error) (Applier, *[]string) {
	t.Helper()
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/install.sh") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	applier := Applier{BaseURL: server.URL, Client: server.Client()}
	if run != nil {
		applier.Run = func(_ context.Context, scriptPath string, output io.Writer) error {
			return run(scriptPath, output)
		}
	}
	return applier, &requested
}

func TestApplyRunsTheInstallerOfTheRequestedTag(t *testing.T) {
	var staged string
	applier, requested := applierFor(t, "#!/bin/bash\n", func(scriptPath string, output io.Writer) error {
		staged = scriptPath
		_, _ = io.WriteString(output, "Downloading hhx v0.4.0...\nInstalled hhx v0.4.0 to /Users/alice/.local/bin/hhx\n")
		return nil
	})
	var shown bytes.Buffer
	applier.Output = &shown
	applied, err := applier.Apply(context.Background(), "v0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if applied != (Applied{Tag: "v0.4.0", Path: "/Users/alice/.local/bin/hhx"}) {
		t.Fatalf("applied=%+v", applied)
	}
	if len(*requested) != 1 || (*requested)[0] != "/v0.4.0/install.sh" {
		t.Fatalf("requested=%v", *requested)
	}
	if !strings.Contains(shown.String(), "Downloading hhx v0.4.0") {
		t.Fatalf("the installer output was not shown: %q", shown.String())
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("the staged installer was left behind: %v", err)
	}
}

func TestApplyReadsInstallPathsWithSpaces(t *testing.T) {
	applier, _ := applierFor(t, "#!/bin/bash\n", func(_ string, output io.Writer) error {
		_, _ = io.WriteString(output, "Installed hhx v0.4.0 to /Users/Ada Lovelace/.local/bin/hhx\n")
		return nil
	})
	applied, err := applier.Apply(context.Background(), "v0.4.0")
	if err != nil || applied.Path != "/Users/Ada Lovelace/.local/bin/hhx" {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestApplyFailures(t *testing.T) {
	failure := errors.New("exit status 1")
	tests := []struct {
		name   string
		body   string
		output string
		runErr error
		want   string
	}{
		{
			name: "installer failed", body: "#!/bin/bash\n", output: "hhx install: checksum verification failed\n",
			runErr: failure, want: "exit status 1",
		},
		{
			name: "nothing replaced", body: "#!/bin/bash\n", output: "Downloading hhx v0.4.0...\n",
			want: "did not report a replaced binary",
		},
		{
			name: "another version", body: "#!/bin/bash\n", output: "Installed hhx v0.3.0 to /Users/alice/.local/bin/hhx\n",
			want: "instead of v0.4.0",
		},
		{name: "empty installer", body: "", want: "is empty"},
		{name: "oversized installer", body: strings.Repeat("#", maxScriptBytes+1), want: "larger than"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			applier, _ := applierFor(t, test.body, func(_ string, output io.Writer) error {
				_, _ = io.WriteString(output, test.output)
				return test.runErr
			})
			_, err := applier.Apply(context.Background(), "v0.4.0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want %q", err, test.want)
			}
		})
	}
}

func TestApplyFailsWhenTheInstallerCannotBeDownloaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	applier := Applier{BaseURL: server.URL, Client: server.Client(), Run: func(context.Context, string, io.Writer) error {
		t.Fatal("the installer ran without being downloaded")
		return nil
	}}
	if _, err := applier.Apply(context.Background(), "v0.4.0"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplyRejectsValuesThatAreNotReleaseTags(t *testing.T) {
	applier, requested := applierFor(t, "#!/bin/bash\n", func(string, io.Writer) error {
		t.Fatal("the installer ran for an invalid tag")
		return nil
	})
	for _, tag := range []string{"", "latest", "../../etc", "v1.2.3-rc.1"} {
		if _, err := applier.Apply(context.Background(), tag); err == nil {
			t.Fatalf("tag %q was accepted", tag)
		}
	}
	if len(*requested) != 0 {
		t.Fatalf("invalid tags reached the server: %v", *requested)
	}
}

// 既定の実行器は bash を起動し、端末と無関係な秘密をインストーラーに渡さない。
func TestRunInstallerIsolatesTheInstaller(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	script := filepath.Join(t.TempDir(), "install.sh")
	body := "if [ -t 0 ]; then echo tty; fi\n" +
		"echo \"token=${GH_TOKEN:-unset} proxy=${HTTPS_PROXY:-unset} home=${HOME:+set}\"\n" +
		"read -r line && echo \"stdin=$line\"\n" +
		"echo \"Installed hhx v0.4.0 to $HOME/.local/bin/hhx\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runInstaller(context.Background(), script, &output); err != nil {
		t.Fatalf("err=%v output=%q", err, output.String())
	}
	got := output.String()
	for _, unwanted := range []string{"tty", "token=secret", "stdin="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("output %q contains %q", got, unwanted)
		}
	}
	if !strings.Contains(got, "token=unset proxy=http://proxy.example:3128 home=set") {
		t.Fatalf("output=%q", got)
	}
}

func TestInstallerEnvironmentAlwaysCarriesPathAndHome(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("HOME", "/Users/alice")
	joined := strings.Join(installerEnvironment(), "\n")
	if !strings.Contains(joined, "PATH="+fallbackPath) || !strings.Contains(joined, "HOME=/Users/alice") {
		t.Fatalf("environment=%q", joined)
	}
}
