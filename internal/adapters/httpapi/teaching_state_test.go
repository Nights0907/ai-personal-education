package httpapi

import (
	"context"
	"github.com/musi-ai/education/internal/query"
	"testing"
)

type taskRecords struct {
	query.Repository
	rows []query.Record
}

func (r taskRecords) List(context.Context, string, string, *query.Cursor, int) ([]query.Record, bool, error) {
	return r.rows, false, nil
}
func TestResumeTeachingAndRetry(t *testing.T) {
	s := Server{Repo: taskRecords{rows: []query.Record{{Input: query.Input{Mode: "review", GuideQuestionID: "q1", ClientRequestID: "old"}}}}}
	in := query.Input{ConversationID: "c", ClientRequestID: "new", Query: "我的思路"}
	if e := s.resumeTeaching(context.Background(), &in); e != nil {
		t.Fatal(e)
	}
	if in.Mode != "review" || in.GuideQuestionID != "q1" {
		t.Fatal(in)
	}
	in.Mode = "solve"
	in.GuideQuestionID = ""
	s.resumeTeaching(context.Background(), &in)
	if in.GuideQuestionID != "" {
		t.Fatal("explicit new question inherited review")
	}
	s.Repo = taskRecords{rows: []query.Record{{Input: query.Input{Mode: "guide", GuideQuestionID: "q2", ClientRequestID: "later"}}, {Input: query.Input{Mode: "guide", ClientRequestID: "retry"}}}}
	in = query.Input{ConversationID: "c", ClientRequestID: "retry", Mode: "guide"}
	s.resumeTeaching(context.Background(), &in)
	if in.GuideQuestionID != "" {
		t.Fatal("retry was changed into a followup", in)
	}
}

func (r taskRecords) Get(_ context.Context, _ string, id string) (query.Record, error) {
	for _, row := range r.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return query.Record{}, query.ErrNotFound
}
func TestRegenerationRestoresOriginalTaskAndRejectsDifferentInput(t *testing.T) {
	s := Server{Repo: taskRecords{rows: []query.Record{{ID: "original", Input: query.Input{ConversationID: "c", Query: "原题", Mode: "guide"}}}}}
	in := query.Input{ConversationID: "c", Query: "原题", Mode: "review", GuideQuestionID: "later", RegenerateOf: "original"}
	if e := s.resumeTeaching(context.Background(), &in); e != nil {
		t.Fatal(e)
	}
	if in.Mode != "guide" || in.GuideQuestionID != "" {
		t.Fatal(in)
	}
	in.ConversationID = "other"
	if e := s.resumeTeaching(context.Background(), &in); e != query.ErrConflict {
		t.Fatal(e)
	}
}
