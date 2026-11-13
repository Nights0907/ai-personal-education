package httpapi

import (
	"encoding/json"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptEditorVersionAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.md")
	body := "<system-prompt><identity>teacher</identity><skill-reminder>load skills</skill-reminder><user-memory-reminder>read memory</user-memory-reminder></system-prompt>"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUSI_SYSTEM_PROMPT_FILE", path)
	run := func(content, revision string) int {
		data, _ := json.Marshal(map[string]string{"content": content, "revision": revision})
		w := httptest.NewRecorder()
		(Server{}).editablePrompt(w, httptest.NewRequest("PUT", "/", strings.NewReader(string(data))))
		return w.Code
	}
	if run("invalid", state.Hash(body)) != 400 {
		t.Fatal("invalid structure accepted")
	}
	if run(body, "stale") != 409 {
		t.Fatal("stale revision accepted")
	}
	updated := strings.Replace(body, "teacher", "updated teacher", 1)
	if run(updated, state.Hash(body)) != 200 {
		t.Fatal("save failed")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != updated {
		t.Fatal("not saved")
	}
}
