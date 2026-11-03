package generatededitguard

import (
	"os"
	"path/filepath"
	"strings"
)

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sedInPlace は sed の引数に -i（--in-place、-i.bak、-Ei のようにまとめた短いオプションを含む）があるかを返す。
func sedInPlace(args []word) bool {
	for _, arg := range args[1:] {
		text := arg.text
		if text == "--" {
			return false
		}
		if strings.HasPrefix(text, "--in-place") {
			return true
		}
		if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
			continue
		}
		for _, letter := range text[1:] {
			// -I は macOS の sed の in-place である。
			if letter == 'i' || letter == 'I' {
				return true
			}
			// -e・-f・-l は残りが値なので、その先の i は -i ではない。
			if letter == 'e' || letter == 'f' || letter == 'l' {
				break
			}
		}
	}
	return false
}

// inPlaceValued は perl・ruby の短いオプションのうち、残りか次の引数を値に取るものである。
var inPlaceValued = map[string]string{"perl": "eEMmIxdC0l", "ruby": "eErIC0xFlKTWU"}

// inPlace は perl・ruby の引数に -i（-pi・-i.bak のようにまとめたものを含む）があるかを返す。
// オプションは最初のオプションでない引数（スクリプトのファイルか、-e が無ければそのファイル）までで、-e の値はその間に挟まる。
func inPlace(args []word, valued string) bool {
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if !strings.HasPrefix(text, "-") || text == "-" || text == "--" {
			return false
		}
		for position, letter := range text[1:] {
			if letter == 'i' {
				return true
			}
			if strings.ContainsRune(valued, letter) {
				// 値がくっついていなければ、次の引数が値である（-pe 'code'）。
				if position == len(text)-2 && (letter == 'e' || letter == 'E') {
					index++
				}
				break
			}
		}
	}
	return false
}

// scriptFiles は perl・ruby -i の対象のファイル（コードの後ろの引数）を返す。-e が無ければ最初の引数はスクリプトである。
func scriptFiles(args []word) []word {
	hasCode := false
	var files []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "-e" || text == "-E":
			hasCode = true
			index++
		case strings.HasPrefix(text, "-") && text != "-":
			if strings.Contains(strings.TrimLeft(text, "-"), "e") {
				hasCode = true
				if strings.HasSuffix(text, "e") {
					index++
				}
			}
		default:
			files = append(files, args[index])
		}
	}
	if !hasCode && len(files) > 0 {
		files = files[1:]
	}
	return files
}

var awkNames = map[string]bool{"awk": true, "gawk": true}

// awkInPlace は gawk の -i inplace（--include=inplace）があるかを返す。
func awkInPlace(args []word) bool {
	for index := 1; index < len(args); index++ {
		text := args[index].text
		next := ""
		if index+1 < len(args) {
			next = args[index+1].text
		}
		if ((text == "-i" || text == "--include") && next == "inplace") || text == "-iinplace" ||
			text == "--include=inplace" {
			return true
		}
	}
	return false
}

// awkFiles は gawk -i inplace の対象のファイルを返す。-f / -e が無ければ最初の引数はプログラムである。
func awkFiles(args []word) []word {
	valued := map[string]bool{
		"-f": true, "-v": true, "-i": true, "-e": true, "-F": true, "--include": true,
		"--file": true, "--source": true, "--assign": true, "--field-separator": true,
	}
	program := false
	for _, arg := range args[1:] {
		if arg.text == "-f" || arg.text == "-e" || arg.text == "--file" || arg.text == "--source" {
			program = true
		}
	}
	files := operands(args, valued)
	if !program && len(files) > 0 {
		files = files[1:]
	}
	var out []word
	for _, file := range files {
		if !strings.Contains(file.text, "=") {
			out = append(out, file)
		}
	}
	return out
}

// copyTargets は cp・mv・install の書き込み先を足す。最後の引数（-t があればその値）がディレクトリなら、その下の同名のファイルにする。
func (s *bashScan) copyTargets(name string, args []word) {
	valued := map[string]bool{"-t": true, "-S": true, "--target-directory": true, "--suffix": true}
	if name != "cp" && name != "mv" {
		valued = map[string]bool{
			"-t": true, "-S": true, "-m": true, "-o": true, "-g": true, "--mode": true,
			"--owner": true, "--group": true, "--target-directory": true, "--suffix": true,
		}
	}
	var directory *word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if text == "-d" && name != "cp" && name != "mv" {
			// install -d はディレクトリを作るだけである。
			return
		}
		switch {
		case (text == "-t" || text == "--target-directory") && index+1 < len(args):
			directory = &args[index+1]
		case strings.HasPrefix(text, "--target-directory="):
			directory = &word{text: text[len("--target-directory="):], dynamic: args[index].dynamic}
		case strings.HasPrefix(text, "-t") && len(text) > 2:
			directory = &word{text: text[2:], dynamic: args[index].dynamic}
		}
	}
	files := operands(args, valued)
	if directory == nil {
		if len(files) < 2 {
			return
		}
		destination := files[len(files)-1]
		files = files[:len(files)-1]
		resolved := s.resolve(destination)
		if len(resolved) != 1 || !isDir(resolved[0]) {
			for _, path := range resolved {
				s.addCopy(path, s.sources(files))
			}
			return
		}
		directory = &destination
	}
	resolved := s.resolve(*directory)
	if len(resolved) != 1 {
		return
	}
	for _, file := range files {
		if !file.dynamic {
			s.addCopy(filepath.Join(resolved[0], filepath.Base(file.text)), s.sources([]word{file}))
		}
	}
}

// sources はコピーの送り元を解決する。静的に決まらない送り元があれば、空の送り元（生成ファイルとみなさない）を返す。
func (s *bashScan) sources(files []word) []string {
	var out []string
	for _, file := range files {
		resolved := s.resolve(file)
		if len(resolved) == 0 {
			return []string{""}
		}
		out = append(out, resolved...)
	}
	return out
}
