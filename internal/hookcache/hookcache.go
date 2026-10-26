// Package hookcache は、hook が失っても害のない状態（取得のキャッシュ、注入済みの記録、拒否したコマンドの記録）を置くディレクトリを決める。
//
// 置き場所は `$XDG_CACHE_HOME/hhx/<hook 名>`（XDG_CACHE_HOME が絶対パスでなければ `~/.cache/hhx/<hook 名>`）である。
// 消えても、再注入されるか compact 後の復元が 1 回欠けるか、取りこぼしを調べる材料が減るだけなので、移行や退避はしない。
package hookcache

import (
	"errors"
	"os"
	"path/filepath"
)

// Dir は hook の状態を置くディレクトリを返す。作りはしない（書き込むときに MkdirAll で 0700 にして作る）。
func Dir(hook string) (string, error) {
	root, err := root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, hook), nil
}

// MkdirAll は dir とその親を 0700 で作る。
func MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// WriteFile は dir に name のファイルを 0600 で書く。途中で止まっても壊れたファイルを残さないよう、一時ファイルから置き換える。
func WriteFile(dir, name string, data []byte) error {
	if err := MkdirAll(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, name))
}

// root は hhx のキャッシュの根のディレクトリを返す。
func root() (string, error) {
	// XDG Base Directory の仕様どおり、相対パスの XDG_CACHE_HOME は無視する。
	if base := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(base) {
		return filepath.Join(base, "hhx"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("the home directory is not an absolute path")
	}
	return filepath.Join(home, ".cache", "hhx"), nil
}
