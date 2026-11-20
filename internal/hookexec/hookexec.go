// Package hookexec は、hook から外部コマンド（git・gh）を時間の上限付きで起動する。
//
// gh は PATH から探して子プロセスで起動する。認証を gh に任せ、テストでは PATH の先頭に置いた偽の gh で差し替えるためである。
package hookexec

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// waitDelay は時間切れで止めた後、子孫のプロセスが出力を開いたままでも待ち続けない猶予である。
const waitDelay = time.Second

// Output は dir で name を起動し、stdout をそのまま返す。
// 起動に失敗した・時間切れになった・0 以外で終わったときは ok が偽になる（移植元の subprocess.run の例外と returncode を 1 つにまとめた）。
// dir が空ならこのプロセスの作業ディレクトリで起動する。stdin は渡さない。
func Output(dir string, timeout time.Duration, name string, args ...string) (stdout string, ok bool) {
	return OutputWithEnv(dir, timeout, nil, name, args...)
}

// OutputWithEnv は Output と同じ方法で起動し、env の KEY=value を子プロセスの環境に適用する。
func OutputWithEnv(dir string, timeout time.Duration, env []string, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	if len(env) > 0 {
		command.Env = append(command.Environ(), env...)
	}
	command.WaitDelay = waitDelay
	var out bytes.Buffer
	command.Stdout = &out
	if err := command.Run(); err != nil {
		return "", false
	}
	return out.String(), true
}
