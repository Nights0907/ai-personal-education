package afs_test

import (
	"context"
	"github.com/cloudwego/eino/components/tool"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	"github.com/musi-ai/education/internal/agent/runtime/afs/inmem"
	"strings"
	"testing"
)

func TestReadOnlyMountIsolation(t *testing.T) {
	fs, e := inmem.New(map[string][]byte{"/skills/math/SKILL.md": []byte("authorized")})
	if e != nil {
		t.Fatal(e)
	}
	read := afs.Tools(fs)[0].(tool.InvokableTool)
	if out, e := read.InvokableRun(context.Background(), `{"path":"/skills/math/SKILL.md"}`); e != nil || out != "authorized" {
		t.Fatal(out, e)
	}
	for _, args := range []string{`{"path":"/skills/../secret"}`, `{"path":"C:\\secret"}`, `{"path":"/skills/math/SKILL.md","command":"write"}`} {
		if _, e := read.InvokableRun(context.Background(), args); e == nil {
			t.Fatal("unauthorized input accepted", args)
		}
	}
}

func TestUnavailableFileReturnsCorrectionWithoutContent(t *testing.T) {
	fs, _ := inmem.New(map[string][]byte{"/skills/math/SKILL.md": []byte("authorized")})
	read := afs.Tools(fs)[0].(tool.InvokableTool)
	out, err := read.InvokableRun(context.Background(), `{"path":"/student-b/profile"}`)
	if err != nil || !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "read_student_memory") {
		t.Fatalf("expected safe corrective tool result: %s %v", out, err)
	}
}
