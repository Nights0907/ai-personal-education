package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `<system-prompt>
<identity>
## 数学教师
允许 Markdown：x < 0 与 A & B。
</identity>
<skill-reminder>按清单加载技能</skill-reminder>
<user-memory-reminder>只使用已授权资料</user-memory-reminder>
</system-prompt>`

func TestReadEditableMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(path, []byte("\ufeff"+strings.ReplaceAll(valid, "\n", "\r\n")), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := Read(path)
	if err != nil || text != valid {
		t.Fatal("Markdown/math or line endings altered", err)
	}
	revised := strings.Replace(valid, "数学教师", "高中数学教师", 1)
	if err = os.WriteFile(path, []byte(revised), 0600); err != nil {
		t.Fatal(err)
	}
	text, err = Read(path)
	if err != nil || text != revised {
		t.Fatal("file edit was not reread", err)
	}
	t.Setenv(EnvironmentKey, path)
	resolved, err := Resolve("")
	if err != nil || resolved != path {
		t.Fatal(resolved, err)
	}
	if _, err = Read(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("missing file silently fell back")
	}
}

func TestRejectInvalidMarkdownBeforeModelUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.md")
	for _, text := range []string{"", "# unframed", strings.Replace(valid, "</identity>", "", 1), strings.Replace(valid, "<skill-reminder>", "<identity>", 1), valid + "\x00", string([]byte{0xff}), strings.Repeat("x", MaxFileBytes+1)} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Fatal("invalid system prompt accepted")
		}
	}
	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("directory accepted as prompt")
	}
}
