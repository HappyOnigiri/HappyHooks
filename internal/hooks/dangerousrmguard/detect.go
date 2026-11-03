package dangerousrmguard

import (
	"regexp"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- 判定 ------------------------------------------------------------------

// findDangerousRemoval は空になりうる変数を対象にした削除を見つけたら、コマンド名と対象を返す。
func findDangerousRemoval(command string) (name, target string) {
	if !strings.Contains(command, "$") || !rmWordRE.MatchString(command) {
		return "", ""
	}
	for _, segment := range removalSegments(command, " ") {
		for index := 0; index < len(segment.args); index++ {
			arg := trailingClosersRE.ReplaceAllLiteralString(segment.args[index], "")
			switch {
			case arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "'"):
			case redirectHeadRE.MatchString(arg):
				if redirectOnlyRE.MatchString(arg) {
					index++
				}
			case a6vRE.MatchString(arg):
				return segment.name, arg
			}
		}
	}
	return "", ""
}

// findDerivedVariableRemoval は、危険な形の右辺を代入した変数を後で丸ごと削除する形を見つける。
// v2.1.283 は D=$S/$n; rm -rf $D を、代入元の $S が空ならルート直下になるとして ask にする。
// 通常の rm -rf $DIR は従来どおり通し、代入の後の削除だけを対象にする。
func findDerivedVariableRemoval(command string) (name, target string) {
	derived := make(map[string]bool)
	for _, segment := range segmentRE.Split(flatten(command, " "), -1) {
		head := stripLeadingKeywords(py.LStrip(segment))
		if match := assignmentRE.FindStringSubmatch(head); match != nil {
			derived[match[1]] = derivedRootRE.MatchString(match[2])
		}
		for _, removal := range removalSegments(head, " ") {
			for _, arg := range removal.args {
				match := bareVariableRE.FindStringSubmatch(arg)
				if match == nil {
					arg = trailingClosersRE.ReplaceAllLiteralString(arg, "")
					match = bareVariableRE.FindStringSubmatch(arg)
				}
				if match == nil {
					continue
				}
				variable := match[1]
				if variable == "" {
					variable = match[2]
				}
				if derived[variable] {
					return removal.name, arg
				}
			}
		}
	}
	return "", ""
}

// findInSubstitution はコマンド置換の中の危険な削除を見つけたら、コマンド名と対象を返す。
func findInSubstitution(command string) (name, target string) {
	if !strings.Contains(command, "$") && !strings.Contains(command, "`") {
		return "", ""
	}
	for _, inner := range substitutions(command) {
		if name, target := findDangerousRemoval(inner); name != "" {
			return name, target
		}
	}
	return "", ""
}

// resolvedTarget は削除対象 1 つを解決したものである。
type resolvedTarget struct {
	token string
	// target はクォートと ~・$HOME を解いたもの、absolute は cwd を基準にした絶対パス、stripped は末尾の glob を除いたもの。
	target, absolute, stripped string
	isRelative                 bool
}

// targets は断片の位置引数を解決して返す。空になった対象は除く。
func targets(segment removal, cwd string) []resolvedTarget {
	var nonEmpty []string
	for _, arg := range segment.args {
		if arg != "" {
			nonEmpty = append(nonEmpty, arg)
		}
	}
	var resolved []resolvedTarget
	for _, token := range positionalArgs(dropRedirects(nonEmpty)) {
		target := unquote(trailingClosersRE.ReplaceAllLiteralString(expandKnownVars(token), ""))
		if target == "" {
			continue
		}
		absolute := target
		if !py.IsAbs(target) {
			absolute = py.Normpath(py.Join(cwd, target))
		}
		resolved = append(resolved, resolvedTarget{
			token: token, target: target, absolute: absolute, stripped: stripGlobTail(absolute),
			isRelative: !py.IsAbs(target),
		})
	}
	return resolved
}

// remainder は相対指定の対象から cwd の部分を除いた残りである（絶対指定ならそのまま）。
func (t resolvedTarget) remainder(cwd string) string {
	if !t.isRelative {
		return t.stripped
	}
	prefix := cwd
	if !strings.HasSuffix(cwd, "/") {
		prefix += "/"
	}
	switch {
	case strings.HasPrefix(t.stripped, prefix):
		return t.stripped[len(prefix):]
	case t.stripped == cwd:
		return ""
	default:
		return t.stripped
	}
}

// findUnresolvableTarget は静的に解決できない削除対象を見つけたら、種別・対象・解決後を返す（V7r の分岐 A/B/D）。
// 種別は "cd" / "cmdsub_glob" / "shape" / "glob" のいずれかで、組み込みが分けている理由文に対応する。
func findUnresolvableTarget(command, cwd string) (kind, token, resolved string) {
	hasCD := hasDirectoryChange(command)
	for _, segment := range removalSegments(command, cmdsubToken) {
		for _, target := range targets(segment, cwd) {
			if kind, resolved := classifyUnresolvable(segment, target, cwd, hasCD); kind != "" {
				return kind, target.token, resolved
			}
		}
	}
	return "", "", ""
}

func classifyUnresolvable(segment removal, t resolvedTarget, cwd string, hasCD bool) (kind, resolved string) {
	if t.stripped == t.absolute {
		return "", ""
	}
	endsWithGlob := strings.HasSuffix(t.absolute, "/*")
	// 分岐 A: cd と相対 glob の併用。最終的な作業ディレクトリが確定しない。
	// 基準そのものが不明なので、解決後のパスは示さない（示すと嘘になる）。
	if hasCD && t.isRelative && endsWithGlob {
		return "cd", ""
	}
	// 分岐 B のうち `e_(p)` 相当。組み込みは「対象が置換または追跡できない変数展開を含む」で ask にする。
	// 置換は hook からも確実に見えるので、そちらだけ移植する（変数展開の側は追跡状態が見えず誤爆するため未移植）。
	if strings.Contains(t.target, cmdsubToken) {
		return "cmdsub_glob", ""
	}
	// 分岐 B: `..` や `~` を含む形、rmdir -p、`*/` で終わる相対指定。
	if escapesUpward(t.target) || strings.HasPrefix(t.target, "~") ||
		(t.isRelative && dotdotRE.MatchString(t.target) && endsWithGlob) ||
		(segment.name == "rmdir" && endsWithGlob && anyMatch(rmdirParentsRE, segment.args)) ||
		(t.isRelative && trailingGlobDir.MatchString(t.target) && endsWithGlob) {
		return "shape", t.absolute
	}
	// 分岐 D: glob が複数階層を跨ぐ（静的に列挙できない）。
	if endsWithGlob {
		depth := countComponents(t.absolute) - countComponents(t.stripped)
		globs := 0
		for _, component := range strings.Split(t.remainder(cwd), "/") {
			if globCharE.MatchString(component) {
				globs++
			}
		}
		if depth+globs > 1 {
			return "glob", t.absolute
		}
	}
	return "", ""
}

func anyMatch(pattern *regexp.Regexp, items []string) bool {
	for _, item := range items {
		if pattern.MatchString(item) {
			return true
		}
	}
	return false
}

// countComponents は / で区切った要素のうち、空と . を除いた数である。
func countComponents(path string) int {
	count := 0
	for _, component := range strings.Split(path, "/") {
		if component != "" && component != "." {
			count++
		}
	}
	return count
}

// findProtectedPath は重要ディレクトリ・作業ディレクトリの削除を見つけたら、種別・対象・解決後を返す。
// 種別は "critical" / "workspace" / "cwd" のいずれかである。
func findProtectedPath(command, cwd string) (kind, token, resolved string, err error) {
	bases := []string{cwd}
	resolvedCWD, err := realpath(cwd)
	if err != nil {
		return "", "", "", err
	}
	if resolvedCWD != cwd {
		bases = append(bases, resolvedCWD)
	}
	for _, segment := range removalSegments(command, cmdsubToken) {
		for _, target := range targets(segment, cwd) {
			// glob が残っている対象は、どのパスに当たるか確定しないので分岐 B/D の担当である。
			if target.stripped != target.absolute && globCharE.MatchString(target.remainder(cwd)) {
				continue
			}
			kind, candidate, err := classifyProtected(target.stripped, bases)
			if err != nil || kind != "" {
				return kind, target.token, candidate, err
			}
		}
	}
	return "", "", "", nil
}

func classifyProtected(stripped string, bases []string) (kind, candidate string, err error) {
	candidates := []string{stripped}
	resolved, err := realpath(stripped)
	if err != nil {
		return "", "", err
	}
	if resolved != stripped {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		critical, err := isCriticalPath(candidate)
		if err != nil || critical {
			return "critical", candidate, err
		}
		for _, base := range bases {
			removes, err := removesWorkspace(base, candidate)
			if err != nil {
				return "", "", err
			}
			if !removes {
				continue
			}
			if py.Normpath(candidate) == py.Normpath(base) {
				return "cwd", candidate, nil
			}
			return "workspace", candidate, nil
		}
	}
	return "", "", nil
}
