package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	resources "github.com/musi-ai/education/agent-design"
	memstore "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/business/demo"
	"github.com/musi-ai/education/internal/agent/memory"
	agentruntime "github.com/musi-ai/education/internal/agent/runtime"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

func main() {
	turns := flag.Int("turns", 8, "number of offline demonstration turns")
	promptFile := flag.String("system-prompt-file", "", "read this Markdown system prompt again at each turn")
	flag.Parse()
	if *turns < 1 || *turns > 50 {
		log.Fatal("turns must be 1..50")
	}
	ctx := context.Background()
	store := memstore.New()
	app := demo.New()
	counter := ctxbuild.ConservativeCounter{}
	files, paths, e := resources.SkillMount()
	if e != nil {
		log.Fatal(e)
	}
	modelStub := &demo.Model{UseSkill: true}
	factory := &agentruntime.Factory{Models: func(context.Context, string) (model.BaseChatModel, error) { return modelStub, nil }, Tools: map[string]tool.InvokableTool{"commit_demo": app.Tool()}, FileSystem: files, SkillPaths: paths, Guard: app, Counter: counter}
	policy := state.DefaultPolicy()
	policy.KeepRecentTurns = 2
	policy.TriggerPercent = 1
	manager := &memory.Manager{Store: store, Counter: counter, Summarizer: demo.Summarizer{}}
	spec := state.Spec{Name: "runtime_demo", Version: "demo-v1", Instruction: "This is an offline runtime wiring demonstration. Load step-tutor and then commit_demo. Do not claim to solve or diagnose anything.", ModelRef: "offline", ToolRefs: []string{"commit_demo"}, SkillRefs: []string{"step-tutor"}, TerminalTools: []string{"commit_demo"}, MaxIterations: 4, MaxToolCalls: 4, Timeout: time.Minute, History: policy}
	if *promptFile != "" {
		spec.Instruction = ""
		spec.InstructionFile = *promptFile
	}
	rt, e := agentruntime.New(agentruntime.Config{Store: store, Provider: app, Guard: app, Completion: app, Engine: factory, Specs: map[string]state.Spec{"demo-v1": spec}, Counter: counter, OnContextBudgetExceeded: func(ctx context.Context, scope state.Scope, policy state.Policy) error {
		_, err := manager.Schedule(ctx, scope, policy, true)
		return err
	}})
	if e != nil {
		log.Fatal(e)
	}
	scope := state.Scope{Namespace: "demo", SubjectID: "local-student", ConversationID: "offline-session"}
	for n := 1; n <= *turns; n++ {
		inputRef := fmt.Sprintf("input-%d", n)
		app.Inputs[inputRef] = fmt.Sprintf("第 %d 轮，我在运算步骤处卡住。%s", n, strings.Repeat("这是验证上下文的示例文字。", 4))
		in := state.Input{Identity: state.Identity{Scope: scope, TurnID: fmt.Sprintf("turn-%d", n), RunID: fmt.Sprintf("run-%d", n)}, InputRef: inputRef, BindingRef: inputRef, ConfigRef: "demo-v1", Lease: state.Lease{Token: int64(n), Until: time.Now().Add(time.Minute)}}
		result, e := rt.RunTurn(ctx, in)
		if e != nil {
			log.Fatal(e)
		}
		b, _ := json.Marshal(result)
		fmt.Println(string(b))
		if _, e = manager.DispatchOne(ctx); e != nil {
			log.Fatal(e)
		}
		if _, e = manager.ProcessOne(ctx, "demo-summary-worker"); e != nil {
			log.Fatal(e)
		}
	}
	v, e := store.View(ctx, scope)
	if e != nil {
		log.Fatal(e)
	}
	fmt.Printf("sealed_turns=%d summary_revision=%d summary_through=%d model_calls=%d business_commits=%d\n", len(v.Turns), v.Summary.Revision, v.Summary.ThroughCursor, modelStub.Calls.Load(), app.CommitCalls)
}
