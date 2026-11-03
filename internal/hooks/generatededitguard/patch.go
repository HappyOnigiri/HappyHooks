package generatededitguard

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// --- パッチ -------------------------------------------------------------------

var (
	// diffHeaderRE は unified diff の対象のファイルの行（--- と +++）である。タブより後ろ（日時）は含めない。
	diffHeaderRE = regexp.MustCompile(`(?m)^(?:\+\+\+|---)[ \t]+([^\t\r\n]+)`)
	// gitDiffRE は git の diff の見出し（diff --git a/<path> b/<path>）である。
	gitDiffRE = regexp.MustCompile(`(?m)^diff --git a/(\S+) b/(\S+)`)
	// patchFileRE は apply_patch の形式の対象のファイルの行である。
	patchFileRE = regexp.MustCompile(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): *([^\r\n]+?)[ \t]*\r?$`)
)

// diffPaths は diff のテキストに現れるパスを返す（/dev/null を除く）。
func diffPaths(text string) []string {
	var paths []string
	for _, match := range diffHeaderRE.FindAllStringSubmatch(text, -1) {
		path := strings.TrimSpace(match[1])
		if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
			if unquoted, err := strconv.Unquote(path); err == nil {
				path = unquoted
			}
		}
		if path != "/dev/null" && path != "" {
			paths = append(paths, path)
		}
	}
	for _, match := range gitDiffRE.FindAllStringSubmatch(text, -1) {
		paths = append(paths, "a/"+match[1], "b/"+match[2])
	}
	return paths
}

// stripComponents はパスの先頭から n 個の要素を除く。要素が足りなければ空文字列を返す。
func stripComponents(path string, n int) string {
	for range n {
		index := strings.IndexByte(path, '/')
		if index < 0 {
			return ""
		}
		path = strings.TrimLeft(path[index+1:], "/")
	}
	return path
}

// readSmall は通常のファイルを上限まで読む。読めなければ空文字列を返す。
func readSmall(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	data, _ := io.ReadAll(io.LimitReader(file, maxPatchFileSize))
	return string(data)
}

// stdinText はコマンドの stdin に渡る静的なテキストを返す。ヒアドキュメント・here-string・パイプの前の echo / printf と、
// allowFiles なら < のファイルとパイプの前の cat のファイルも読む。
func (s *bashScan) stdinText(c *command, allowFiles bool) string {
	var texts []string
	for _, r := range c.redirects {
		switch r.op {
		case "<<", "<<-":
			texts = append(texts, r.body)
		case "<<<":
			texts = append(texts, r.target.text)
		case "<":
			if allowFiles {
				for _, path := range s.resolve(r.target) {
					texts = append(texts, readSmall(path))
				}
			}
		}
	}
	if source := c.pipedFrom; source != nil {
		for _, r := range source.redirects {
			switch r.op {
			case "<<", "<<-":
				texts = append(texts, r.body)
			case "<<<":
				texts = append(texts, r.target.text)
			}
		}
		args := stripPrefixes(source.words)
		if len(args) > 0 {
			switch baseName(args[0].text) {
			case "echo", "printf":
				texts = append(texts, joinWords(args[1:]))
			case "cat":
				if allowFiles {
					for _, file := range operands(args, nil) {
						for _, path := range s.resolve(file) {
							texts = append(texts, readSmall(path))
						}
					}
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

// patchTargets は patch の書き込み先を足す。
// 引数に元のファイルがあればそれを、-o があればその出力を、どちらも無ければパッチの中のパスを候補にする。
func (s *bashScan) patchTargets(c *command, args []word) {
	valued := map[string]bool{
		"-p": true, "-d": true, "-i": true, "-o": true, "-B": true, "-D": true, "-F": true,
		"-g": true, "-r": true, "-V": true, "-Y": true, "-z": true, "--strip": true, "--directory": true,
		"--input": true, "--output": true,
	}
	strip := -1
	directory := s.cwd
	var inputs, outputs []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		next := word{}
		if index+1 < len(args) {
			next = args[index+1]
		}
		switch {
		case text == "-p" || text == "--strip":
			strip, _ = strconv.Atoi(next.text)
		case strings.HasPrefix(text, "-p"):
			strip, _ = strconv.Atoi(text[2:])
		case strings.HasPrefix(text, "--strip="):
			strip, _ = strconv.Atoi(text[len("--strip="):])
		case text == "-d" || text == "--directory":
			if resolved := s.resolve(next); len(resolved) == 1 {
				directory = resolved[0]
			}
		case text == "-i" || text == "--input":
			inputs = append(inputs, next)
		case text == "-o" || text == "--output":
			outputs = append(outputs, next)
		}
	}
	if len(outputs) > 0 {
		s.addWords(outputs)
		return
	}
	files := operands(args, valued)
	if len(files) > 0 {
		s.addWords(files[:1])
		if len(files) > 1 {
			inputs = append(inputs, files[1])
		}
	}
	text := s.stdinText(c, true)
	for _, input := range inputs {
		for _, path := range s.resolve(input) {
			text += "\n" + readSmall(path)
		}
	}
	for _, path := range diffPaths(text) {
		candidates := []string{path, stripComponents(path, 1), filepath.Base(path)}
		if strip >= 0 {
			candidates = []string{stripComponents(path, strip)}
		}
		for _, candidate := range candidates {
			if candidate != "" {
				s.add(joinUnder(directory, candidate))
			}
		}
	}
}

// joinUnder は path を directory から解決する（絶対パスならそのまま）。
func joinUnder(directory, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(directory, path)
}

// gitTargets は git の大域オプション（-C を含む）を読み、git apply の書き込み先を足す。
func (s *bashScan) gitTargets(c *command, args []word) {
	directory := s.cwd
	index := 1
	for ; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "-C" && index+1 < len(args):
			index++
			resolved := s.resolveFrom(directory, args[index])
			if resolved == "" {
				return
			}
			directory = resolved
		case text == "-c" || text == "--git-dir" || text == "--work-tree" || text == "--namespace":
			index++
		case strings.HasPrefix(text, "-"):
		default:
			if text == "apply" {
				s.gitApplyTargets(c, args[index:], directory)
			}
			return
		}
	}
}

// resolveFrom は語を directory から解決する。静的に決まらなければ空文字列を返す。
func (s *bashScan) resolveFrom(directory string, w word) string {
	saved := s.cwd
	s.cwd = directory
	resolved := s.resolve(w)
	s.cwd = saved
	if len(resolved) != 1 {
		return ""
	}
	return resolved[0]
}

// gitApplyTargets は git apply のパッチの中のパスを、作業ツリーの根と作業ディレクトリの両方から解決して足す。
// 作業ツリーの中では git apply のパスは根からの相対だが、外では作業ディレクトリからの相対になるため両方を候補にする。
func (s *bashScan) gitApplyTargets(c *command, args []word, directory string) {
	strip := 1
	root := ""
	onlyIndex, report, apply := false, false, false
	var files []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "--check" || text == "--stat" || text == "--numstat" || text == "--summary":
			report = true
		case text == "--apply":
			apply = true
		case text == "--cached":
			onlyIndex = true
		case strings.HasPrefix(text, "-p") && len(text) > 2:
			strip, _ = strconv.Atoi(text[2:])
		case text == "-p" && index+1 < len(args):
			index++
			strip, _ = strconv.Atoi(args[index].text)
		case strings.HasPrefix(text, "--directory="):
			root = text[len("--directory="):]
		case text == "-" || !strings.HasPrefix(text, "-"):
			files = append(files, args[index])
		}
	}
	if (report && !apply) || onlyIndex {
		return
	}
	text := ""
	if len(files) == 0 || slices.ContainsFunc(files, func(w word) bool { return w.text == "-" }) {
		text = s.stdinText(c, true)
	}
	for _, file := range files {
		if path := s.resolveFrom(directory, file); path != "" {
			text += "\n" + readSmall(path)
		}
	}
	top := worktreeTop(directory)
	for _, path := range diffPaths(text) {
		stripped := stripComponents(path, strip)
		if stripped == "" {
			continue
		}
		if root != "" {
			stripped = filepath.Join(root, stripped)
		}
		s.add(joinUnder(directory, stripped))
		if top != "" {
			s.add(joinUnder(top, stripped))
		}
	}
}

// worktreeTop は directory から親へ .git を探し、作業ツリーの根を返す。見つからなければ空文字列を返す。
// git を起動しないのは、全 Bash 呼び出しで走る hook を軽く保つためである。
func worktreeTop(directory string) string {
	for current := directory; ; {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// applyPatchTargets はシェルから起動した apply_patch（Codex の形式）のパッチの中のパスを足す。
func (s *bashScan) applyPatchTargets(c *command, args []word) {
	text := s.stdinText(c, false) + "\n" + joinWords(args[1:])
	for _, match := range patchFileRE.FindAllStringSubmatch(text, -1) {
		s.add(s.absolute(match[1]))
	}
}
