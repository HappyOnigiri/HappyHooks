package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// maxScriptBytes は取得するインストーラーの上限である。配布している script は数 KiB に収まる。
	maxScriptBytes = 1 << 20
	// downloadTimeout はインストーラーの取得にかける上限である。
	downloadTimeout = 30 * time.Second
	// DefaultRunTimeout はインストーラーの実行にかける上限である。バイナリの取得と検証を含むので、確認より長く取る。
	DefaultRunTimeout = 10 * time.Minute
	// fallbackPath は PATH が空の環境で install.sh が要るコマンドを探す場所である。
	fallbackPath = "/usr/bin:/bin:/usr/sbin:/sbin"
)

// installedPattern は install.sh が報告する配置先を読む。配置先は行末まで取り、空白を含む HOME でも取りこぼさない。
var installedPattern = regexp.MustCompile(`(?m)^Installed hhx (\S+) to (.+)$`)

// forwardedEnvironment は install.sh の中の curl が外へ出るのに要る設定である。
// トークンなどの無関係な秘密をインストーラーへ渡さないよう、許可リストで引き継ぐ。
var forwardedEnvironment = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "TMPDIR",
}

// Applied は install.sh が報告した置き換えの結果である。
type Applied struct {
	// Tag は入ったリリースタグ。
	Tag string
	// Path は置き換えたバイナリの配置先。
	Path string
}

// Applier は指定したリリースの install.sh を取得して実行する。
type Applier struct {
	// BaseURL は空なら DownloadBase を使う。
	BaseURL string
	// Client は空なら downloadTimeout を持つ client を使う。
	Client *http.Client
	// RunTimeout は 0 なら DefaultRunTimeout を使う。
	RunTimeout time.Duration
	// Output はインストーラーの出力を流す先である。空なら捨てる。
	Output io.Writer
	// Run はインストーラーの実行だけを差し替える点である。空なら bash で実行する。
	Run func(ctx context.Context, scriptPath string, output io.Writer) error
}

// Apply は tag の install.sh を取得して実行し、置き換えの報告を確かめる。
// 検証と置換は install.sh が持つので、ここでは取得と実行と報告の照合だけを行う。
func (a Applier) Apply(ctx context.Context, tag string) (Applied, error) {
	// 取得元の URL に任意の文字列を差し込ませない。
	if _, ok := ParseVersion(tag); !ok {
		return Applied{}, fmt.Errorf("%q is not a release tag", tag)
	}
	script, err := a.download(ctx, tag)
	if err != nil {
		return Applied{}, err
	}
	directory, err := os.MkdirTemp("", "hhx-update-")
	if err != nil {
		return Applied{}, fmt.Errorf("create the update workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	scriptPath := filepath.Join(directory, "install.sh")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		return Applied{}, fmt.Errorf("stage the installer: %w", err)
	}
	timeout := a.RunTimeout
	if timeout <= 0 {
		timeout = DefaultRunTimeout
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := a.Run
	if run == nil {
		run = runInstaller
	}
	var captured bytes.Buffer
	output := io.Writer(&captured)
	if a.Output != nil {
		output = io.MultiWriter(a.Output, &captured)
	}
	if err := run(runContext, scriptPath, output); err != nil {
		return Applied{}, fmt.Errorf("the installer for %s failed: %w", tag, err)
	}
	return appliedResult(tag, captured.String())
}

func (a Applier) download(ctx context.Context, tag string) ([]byte, error) {
	base := a.BaseURL
	if base == "" {
		base = DownloadBase
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+tag+"/install.sh", nil)
	if err != nil {
		return nil, fmt.Errorf("build the installer request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download the installer for %s: %w", tag, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download the installer for %s: GitHub responded %s", tag, response.Status)
	}
	// 上限より 1 byte 多く読み、切り詰めた script を bash へ渡さない。
	// 黙って切ると、取得の破損が bash の構文エラーという無関係な失敗として現れる。
	body, err := io.ReadAll(io.LimitReader(response.Body, maxScriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download the installer for %s: %w", tag, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("the installer for %s is empty", tag)
	}
	if len(body) > maxScriptBytes {
		return nil, fmt.Errorf("the installer for %s is larger than %d bytes", tag, maxScriptBytes)
	}
	return body, nil
}

// appliedResult は install.sh の報告から置き換えを確かめる。報告が無ければ置き換えは起きていない。
func appliedResult(tag, output string) (Applied, error) {
	matches := installedPattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return Applied{}, fmt.Errorf("the installer for %s did not report a replaced binary", tag)
	}
	last := matches[len(matches)-1]
	if last[1] != tag {
		return Applied{}, fmt.Errorf("the installer reported %s instead of %s", last[1], tag)
	}
	return Applied{Tag: tag, Path: strings.TrimSpace(last[2])}, nil
}

// runInstaller は install.sh を bash で実行する。標準入力を閉じ、環境は許可リストだけを渡し、
// 新しいセッションで起動して制御端末を継承させない。hook の実行環境や agent の端末から
// 秘密や入力待ちがインストーラーへ漏れないためである。
func runInstaller(ctx context.Context, scriptPath string, output io.Writer) error {
	//nolint:forbidigo // hook ではなく hhx update の実体で、環境の許可リストと新しいセッションを指定して起動する
	command := exec.CommandContext(ctx, "bash", scriptPath)
	command.Env = installerEnvironment()
	command.Stdin = nil
	command.SysProcAttr = installerProcessAttributes()
	command.Stdout = output
	command.Stderr = output
	// 上限を超えたら bash を止め、出力を握ったままの子プロセスを待ち続けない。
	command.WaitDelay = time.Second
	return command.Run()
}

// installerEnvironment は install.sh に渡す環境を作る。PATH と HOME は常に渡す。
func installerEnvironment() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = fallbackPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	environment := []string{"PATH=" + path, "HOME=" + home}
	for _, name := range forwardedEnvironment {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
