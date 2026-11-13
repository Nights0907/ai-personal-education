package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	resources "github.com/musi-ai/education/agent-design"
	memstore "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/business/demo"
	runtime "github.com/musi-ai/education/internal/agent/runtime"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/prompt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
)

func editablePrompt(revision string) string {
	return "<system-prompt>\n<identity>## " + revision + "</identity>\n<skill-reminder>Load the authorized skill</skill-reminder>\n<user-memory-reminder>Use authorized memory</user-memory-reminder>\n</system-prompt>"
}

type promptModel struct {
	base *demo.Model
	path string
	seen []string
}

func (m *promptModel) Generate(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.seen = append(m.seen, ms[0].Content)
	if len(m.seen) == 1 {
		if err := os.WriteFile(m.path, []byte(editablePrompt("Revision B")), 0600); err != nil {
			return nil, err
		}
	}
	return m.base.Generate(ctx, ms, opts...)
}
func (*promptModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not used")
}

func TestMarkdownReloadsNextTurnAndFreezesCurrentRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(path, []byte(editablePrompt("Revision A")), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(prompt.EnvironmentKey, path)
	app := demo.New()
	counter := ctxbuild.ConservativeCounter{}
	s := &captureStore{Store: memstore.New()}
	m := &promptModel{base: &demo.Model{UseSkill: true}, path: path}
	skills, err := resources.Skills()
	if err != nil {
		t.Fatal(err)
	}
	f := &runtime.Factory{Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Tools: map[string]tool.InvokableTool{"commit_demo": app.Tool()}, Skills: skills, Guard: app, Counter: counter}
	// No inline instruction/path: the project Markdown default (overridden by
	// the server-owned environment variable here) is selected at startup.
	spec := state.Spec{Name: "test", Version: "file-v1", ModelRef: "fake", SkillRefs: []string{"step-tutor"}, ToolRefs: []string{"commit_demo"}, TerminalTools: []string{"commit_demo"}, MaxIterations: 4, MaxToolCalls: 4, Timeout: time.Minute, History: state.DefaultPolicy()}
	rt, err := runtime.New(runtime.Config{Store: s, Provider: app, Guard: app, Completion: app, Engine: f, Specs: map[string]state.Spec{"test-v1": spec}, Counter: counter})
	if err != nil {
		t.Fatal(err)
	}
	scope := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "prompt-edit"}
	ctx := context.Background()
	for n := 1; n <= 2; n++ {
		in := contracttest.Input(scope, n)
		app.Inputs[in.InputRef] = "x+1=2"
		result, e := rt.RunTurn(ctx, in)
		if e != nil || result.HistoryStatus != "sealed" {
			t.Fatal(result, e)
		}
	}
	if len(m.seen) != 4 {
		t.Fatal("expected two model calls per turn", len(m.seen))
	}
	for i, text := range m.seen {
		want := "Revision A"
		if i >= 2 {
			want = "Revision B"
		}
		if !strings.Contains(text, want) {
			t.Fatal("prompt changed inside a run or was not reloaded", i, text)
		}
	}
	if s.snaps[0].PromptHash == s.snaps[3].PromptHash || s.snaps[0].PromptSource != path {
		t.Fatal("prompt source/revision not captured")
	}
	if err = os.WriteFile(path, []byte("broken edit"), 0600); err != nil {
		t.Fatal(err)
	}
	in := contracttest.Input(scope, 3)
	app.Inputs[in.InputRef] = "another question"
	result, err := rt.RunTurn(ctx, in)
	if err == nil || result.ErrorCode != "PROMPT_UNAVAILABLE" || len(m.seen) != 4 || app.CommitCalls != 2 {
		t.Fatal("invalid edit reached model/tools", result, err)
	}
}
