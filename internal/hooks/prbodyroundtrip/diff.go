package prbodyroundtrip

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	contextLines = 3
	compareLines = 64
	previewLines = 24
	maxLineBytes = 240
)

type diffLine struct {
	prefix byte
	text   string
}

// diffPreview は最初の差分を含む unified diff の抜粋を返す。
func diffPreview(current, sent string) (string, bool) {
	var old, next []string
	if current != "" {
		old = strings.Split(current, "\n")
	}
	if sent != "" {
		next = strings.Split(sent, "\n")
	}
	same := 0
	for same < min(len(old), len(next)) && old[same] == next[same] {
		same++
	}
	start := max(0, same-contextLines)
	lines := make([]diffLine, 0, previewLines)
	for _, line := range old[start:same] {
		lines = append(lines, diffLine{' ', line})
	}

	// 差の先頭から限られた行数だけ突き合わせ、本文が大きくても計算量を抑える。
	delta := lineChanges(old[same:min(same+compareLines, len(old))], next[same:min(same+compareLines, len(next))])
	equal := 0
	for _, line := range delta {
		lines = append(lines, line)
		if line.prefix == ' ' {
			equal++
			if equal > 2*contextLines {
				break
			}
		} else {
			equal = 0
		}
	}
	lines = lines[:min(len(lines)-max(0, equal-contextLines), previewLines)]

	var content strings.Builder
	oldCount, nextCount := 0, 0
	shortened := false
	for _, line := range lines {
		if line.prefix != '+' {
			oldCount++
		}
		if line.prefix != '-' {
			nextCount++
		}
		content.WriteByte(line.prefix)
		if len(line.text) > maxLineBytes {
			end := maxLineBytes
			for !utf8.RuneStart(line.text[end]) {
				end--
			}
			content.WriteString(line.text[:end])
			content.WriteRune('…')
			shortened = true
		} else {
			content.WriteString(line.text)
		}
		content.WriteByte('\n')
	}
	shortened = shortened || !slices.Equal(old[start+oldCount:], next[start+nextCount:])
	return fmt.Sprintf("--- GitHub\n+++ --body-file\n@@ -%s +%s @@\n%s",
		diffRange(start, oldCount), diffRange(start, nextCount), content.String()), shortened
}

// lineChanges は共通する行をなるべく残し、行単位の挿入・削除を求める。
func lineChanges(old, next []string) []diffLine {
	var common [compareLines + 1][compareLines + 1]uint8
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(next) - 1; j >= 0; j-- {
			if old[i] == next[j] {
				common[i][j] = 1 + common[i+1][j+1]
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	lines := make([]diffLine, 0, len(old)+len(next))
	for i, j := 0, 0; i < len(old) || j < len(next); {
		switch {
		case i < len(old) && j < len(next) && old[i] == next[j]:
			lines = append(lines, diffLine{' ', old[i]})
			i++
			j++
		case i < len(old) && (j == len(next) || common[i+1][j] >= common[i][j+1]):
			lines = append(lines, diffLine{'-', old[i]})
			i++
		default:
			lines = append(lines, diffLine{'+', next[j]})
			j++
		}
	}
	return lines
}

func diffRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprint(start + 1)
	default:
		return fmt.Sprintf("%d,%d", start+1, count)
	}
}
