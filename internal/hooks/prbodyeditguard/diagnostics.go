package prbodyeditguard

import (
	"iter"
	"regexp"
	"strings"
)

type finding struct {
	start, end int
	id, edge   string
}

var (
	ansiColor       = regexp.MustCompile(`\x1b\[[0-9:;]*m`)
	diagnosticLabel = regexp.MustCompile(`(?i)^[ \t]*` +
		`(?:hint|notice|info|warn(?:ing)?|error|fatal|debug|trace):[ \t]+\S`)
	commandLine            = regexp.MustCompile(`^(?:[ \t]{2,}|\t)(?:\$[ \t]+\S+|[\w./-]+[ \t]+(?:\S+[ \t]+)*--?[a-zA-Z])`)
	diagnosticContinuation = regexp.MustCompile(`^(?:[ \t]*[-*][ \t]+\S|(?:[ \t]{2,}|\t)\S)`)
	logLine                = regexp.MustCompile(`^[ \t]*([\w.-]+):[ \t]+` +
		`(?i:trace|debug|info|warn(?:ing)?|error|fatal|loading|export(?:ing)?|` +
		`starting|finished|failed|running|fetching|reading|writing)\b`)
)

// edgeFindings は元の最初・最後の非空行が提案に一意に残るとき、その外側を追加行として検査する。
// 元の端を書き換えた場合や同じ行が複数ある場合は、差分の境界を推測しない。
func edgeFindings(current string, proposed []string, excluded []bool) []finding {
	original := splitLines(current)
	first, last := 0, len(original)-1
	for first <= last && strings.TrimSpace(original[first]) == "" {
		first++
	}
	for last > first && strings.TrimSpace(original[last]) == "" {
		last--
	}
	if first > last {
		return nil
	}
	head, tail := -1, -1
	if uniqueLine(original, original[first]) >= 0 {
		head = uniqueLine(proposed, original[first])
	}
	if uniqueLine(original, original[last]) >= 0 {
		tail = uniqueLine(proposed, original[last])
	}
	// 先頭と末尾の行が逆順に移動した場合、両方の境界が曖昧になる。
	if head >= 0 && tail >= 0 && head > tail {
		return nil
	}
	if head <= 0 && (tail < 0 || tail == len(proposed)-1) {
		return nil
	}
	remaining := existingDiagnostics(original, proposed)
	var findings []finding
	for _, edge := range []struct {
		id         string
		start, end int
	}{
		{idHead, 0, head},
		{idTail, tail + 1, len(proposed)},
	} {
		if edge.id == idTail && tail < 0 {
			continue
		}
		for found := range diagnostics(proposed, excluded, edge.start, edge.end) {
			if count, exists := remaining[found.lines(proposed)]; exists && count >= 0 {
				continue
			}
			found.edge = edge.id
			findings = append(findings, found)
			break
		}
	}
	return findings
}

// existingDiagnostics は Markdown の装飾に関係なく、同じ診断行の並びが本文内で増えたかを数える。
func existingDiagnostics(original, proposed []string) map[[4]string]int {
	counts := make(map[[4]string]int)
	for found := range diagnostics(original, nil, 0, len(original)) {
		counts[found.lines(original)]++
	}
	if len(counts) > 0 {
		for found := range diagnostics(proposed, nil, 0, len(proposed)) {
			key := found.lines(proposed)
			if _, exists := counts[key]; exists {
				counts[key]--
			}
		}
	}
	return counts
}

func (f finding) lines(body []string) [4]string {
	var lines [4]string
	copy(lines[:], body[f.start-1:f.end])
	return lines
}

func uniqueLine(lines []string, target string) int {
	index := -1
	for i, line := range lines {
		if line == target {
			if index >= 0 {
				return -1
			}
			index = i
		}
	}
	return index
}

// diagnostics は生の ANSI を先に探し、その後にログのまとまりを上から順に探す。行番号は 1 始まり。
func diagnostics(lines []string, excluded []bool, start, end int) iter.Seq[finding] {
	isExcluded := func(i int) bool {
		return i < len(excluded) && excluded[i]
	}
	return func(yield func(finding) bool) {
		for i := start; i < end; i++ {
			if ansiColor.MatchString(lines[i]) && !yield(finding{start: i + 1, end: i + 1, id: idANSI}) {
				return
			}
		}
		for i := start; i+1 < end; i++ {
			if isExcluded(i) || isExcluded(i+1) {
				continue
			}
			if i+3 < end && !isExcluded(i+2) && !isExcluded(i+3) &&
				diagnosticLabel.MatchString(lines[i]) && commandLine.MatchString(lines[i+1]) &&
				diagnosticContinuation.MatchString(lines[i+2]) && diagnosticContinuation.MatchString(lines[i+3]) {
				if !yield(finding{start: i + 1, end: i + 4, id: idGuidance}) {
					return
				}
			}
			prefix := logPrefix(lines[i])
			if prefix != "" && prefix == logPrefix(lines[i+1]) && !yield(finding{start: i + 1, end: i + 2, id: idLogs}) {
				return
			}
		}
	}
}

func logPrefix(line string) string {
	if match := logLine.FindStringSubmatch(line); match != nil {
		return match[1]
	}
	return ""
}

// excludedLines は明示的な Markdown のコードフェンスと引用を、本文全体の文脈で除外する。
func excludedLines(lines []string) []bool {
	excluded := make([]bool, len(lines))
	var fence byte
	var width int
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		marker, size, tail := fenceMarker(trimmed)
		switch {
		case width > 0:
			excluded[i] = true
			if len(line)-len(trimmed) <= 3 && marker == fence && size >= width && strings.TrimSpace(tail) == "" {
				width = 0
			}
		case len(line)-len(trimmed) <= 3 && size >= 3:
			fence, width = marker, size
			excluded[i] = true
		case strings.HasPrefix(trimmed, ">"):
			excluded[i] = true
		}
	}
	return excluded
}

func fenceMarker(line string) (marker byte, count int, tail string) {
	if len(line) == 0 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, ""
	}
	marker = line[0]
	for count < len(line) && line[count] == marker {
		count++
	}
	return marker, count, line[count:]
}
