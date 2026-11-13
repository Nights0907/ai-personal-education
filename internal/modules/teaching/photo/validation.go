package photo

import (
	"regexp"
	"strconv"
	"strings"
)

var constantEquation = regexp.MustCompile(`^[+\-]?[0-9]+(?:\.[0-9]+)?\s*=\s*[+\-]?[0-9]+(?:\.[0-9]+)?$`)
var mathVariable = regexp.MustCompile(`[a-zA-Z]`)

// ValidateQuestionText does not repair or guess OCR. An isolated contradictory
// constant equation after an unfinished expression is evidence of a broken
// formula and requires a confirmed transcription before formal solving.
func ValidateQuestionText(text string) error {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(strings.ReplaceAll(line, "＝", "="))
		if i == 0 || !constantEquation.MatchString(line) {
			continue
		}
		parts := strings.Split(line, "=")
		a, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		b, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		prior := strings.TrimSpace(lines[i-1])
		if e1 == nil && e2 == nil && a != b && mathVariable.MatchString(prior) && !strings.Contains(prior, "=") && strings.ContainsAny(prior, "+-^²³") {
			return ErrAmbiguousText
		}
	}
	return nil
}
