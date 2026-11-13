// agentic-demo runs the actual four-node graph with an explicitly offline model.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	mem "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/runtime/model/proxy"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"log"
	"time"
)

type offline struct{}

func (offline) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("离线管线校验完成。这不是实际数学解答。", nil), nil
}
func (offline) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not used")
}
func main() {
	ctx := context.Background()
	service, e := proxy.NewService(map[string]model.BaseChatModel{"offline": offline{}})
	if e != nil {
		log.Fatal(e)
	}
	p, e := agentic.New(ctx, agentic.Dependencies{Store: mem.New(), Models: proxy.Resolver(service), Spec: state.Spec{Name: "offline_graph", Version: "offline-v1", Instruction: "Offline infrastructure verification only.", ModelRef: "offline", MaxIterations: 3, MaxToolCalls: 3, Timeout: time.Minute, History: state.DefaultPolicy()}}, agentic.NewRegistry())
	if e != nil {
		log.Fatal(e)
	}
	turn, e := p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "demo", SubjectID: "local", ConversationID: "graph"}, TurnID: "offline-turn"}}, Query: "验证四节点完整链路"}, agentic.SinkFunc(func(_ context.Context, p agentic.Packet) error {
		b, e := json.Marshal(p)
		fmt.Println(string(b))
		return e
	}))
	if e != nil {
		log.Fatal(e)
	}
	fmt.Printf("stages=%v status=%s history=%s\n", turn.Stages, turn.Result.Status, turn.Result.HistoryStatus)
}
