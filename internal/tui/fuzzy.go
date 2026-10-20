package tui

import (
	"strings"
	"unicode"
)

// fuzzyScore matches pattern as a case-insensitive subsequence of text and
// scores the match: consecutive runs and matches at word starts score higher,
// and shorter texts win ties. ok is false when pattern is not a subsequence.
func fuzzyScore(pattern, text string) (score int, ok bool) {
	if pattern == "" {
		return 0, true
	}
	p := []rune(strings.ToLower(pattern))
	t := []rune(strings.ToLower(text))
	orig := []rune(text)
	pi, run := 0, 0
	for ti := 0; ti < len(t) && pi < len(p); ti++ {
		if t[ti] != p[pi] {
			run = 0
			continue
		}
		pi++
		run++
		score += 1 + 2*run
		if ti == 0 || !unicode.IsLetter(orig[ti-1]) && !unicode.IsDigit(orig[ti-1]) {
			score += 6 // start of a word or path segment
		}
	}
	if pi < len(p) {
		return 0, false
	}
	return score*4 - len(t), true
}
