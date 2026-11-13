package teacher

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/query"
	"testing"
)

type records struct {
	query.Repository
	record query.Record
}

func (r records) Get(context.Context, string, string) (query.Record, error) { return r.record, nil }

type receipts struct{}

func (receipts) FindCommittedResult(context.Context, state.Input) (*state.Receipt, error) {
	return nil, nil
}
func TestCompletionRequiresCommitOnlyForNewQuestion(t *testing.T) {
	in := state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "s", ConversationID: "c"}, TurnID: "t", RunID: "r"}}
	ms := []*schema.Message{schema.UserMessage("尝试"), schema.AssistantMessage("下一步呢？", nil)}
	for _, tc := range []struct {
		mode, qid string
		want      bool
	}{{"solve", "", false}, {"guide", "", false}, {"guide", "q", true}, {"review", "q", true}} {
		p := Completion{Queries: records{record: query.Record{Input: query.Input{Mode: tc.mode, GuideQuestionID: tc.qid}}}, Solutions: receipts{}}
		r, e := p.Resolve(context.Background(), in, ms, nil)
		if e != nil {
			t.Fatal(e)
		}
		if (r != nil) != tc.want {
			t.Fatalf("mode=%s qid=%s receipt=%v", tc.mode, tc.qid, r)
		}
	}
}
