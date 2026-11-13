package solve

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewIncompleteJSONAndAllowlist(t *testing.T) {
	raw := `{"candidate":{"question":{"stem":"not projected"},"solution":{"answer":[{"content":"x=2"}],"analysis":"等式两边减去3，\\然后除以2。","steps":[{"title":"移项","content":"2x=4"}],"secret":"do not project"}}}`
	for n := 0; n < len(raw); n++ {
		v := Preview(raw[:n])
		b, e := json.Marshal(v)
		if e != nil || strings.Contains(string(b), "secret") || strings.Contains(string(b), "not projected") {
			t.Fatal(n, string(b), e)
		}
	}
	v := Preview(`{"candidate":{"solution":{"analysis":"正在逐步`)
	if v["analysis"] != "正在逐步" {
		t.Fatal(v)
	}
	if Preview(`{"draft_id":"private"}`) != nil {
		t.Fatal("unrelated tool projected")
	}
	if Preview(strings.Repeat("x", 300000)) != nil {
		t.Fatal("unbounded preview")
	}
}
