package agentdesign

import (
	"strings"
	"testing"
)

func TestFixedSPHasExactlyThreePrincipalSections(t *testing.T) {
	text, err := Instruction()
	if err != nil {
		t.Fatal(err)
	}
	position := 0
	for _, name := range []string{"identity", "skill-reminder", "user-memory-reminder"} {
		open, close := "<"+name+">", "</"+name+">"
		start, end := strings.Index(text, open), strings.Index(text, close)
		if strings.Count(text, open) != 1 || strings.Count(text, close) != 1 || start < position || end < start {
			t.Fatal("missing/invalid Markdown SP section", name)
		}
		position = end + len(close)
	}
}
