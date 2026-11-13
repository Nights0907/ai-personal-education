package solve

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolSchemasExpandAllReferences(t *testing.T) {
	infos, e := toolInfos()
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"get_task_input", "get_knowledge_catalog", "validate_solution", "finalize_solution"} {
		p, e := infos[name].ParamsOneOf.ToJSONSchema()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(p)
		if strings.Contains(string(b), "$ref") {
			t.Fatal("unresolved tool reference", name)
		}
	}
}
func TestCandidateRejectsInvalidStepAndKnowledge(t *testing.T) {
	var c Candidate
	if e := Decode(`{"question":{"stem":"x+1=2","type":"constructed_response","difficulty":"easy","options":[],"subquestions":[]},"solution":{"answer":[{"subquestion_id":null,"content":"x=1"}],"analysis":"两边减1","steps":[{"ordinal":1,"subquestion_id":null,"title":"移项","content":"x=2-1=1"}],"knowledge_points":[{"code":"equations_inequalities","is_primary":true}]}}`, &c); e != nil {
		t.Fatal(e)
	}
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	c.Solution.Steps[0].Ordinal = 2
	if Validate(c) == nil {
		t.Fatal("non-contiguous step accepted")
	}
	c.Solution.Steps[0].Ordinal = 1
	c.Solution.KnowledgePoints[0].Code = "invented"
	if Validate(c) == nil {
		t.Fatal("invented knowledge accepted")
	}
}

func TestValidationFeedbackIdentifiesRepairableStructureErrors(t *testing.T) {
	var c Candidate
	if e := Decode(`{"question":{"stem":"求x","type":"constructed_response","difficulty":"easy","options":[],"subquestions":[]},"solution":{"answer":[{"subquestion_id":null,"content":"x=1"}],"analysis":"移项","steps":[{"ordinal":1,"subquestion_id":null,"title":"移项","content":"x=1"}],"knowledge_points":[{"code":"equations_inequalities","is_primary":true}]}}`, &c); e != nil {
		t.Fatal(e)
	}
	c.Question.Difficulty = ""
	if e := Validate(c); e == nil || !strings.Contains(e.Error(), "question.difficulty") || !strings.Contains(e.Error(), "easy") {
		t.Fatal("missing actionable difficulty feedback", e)
	}
	c.Question.Difficulty = "easy"
	c.Solution.Answer = append(c.Solution.Answer, Answer{Content: "y=2"})
	if e := Validate(c); e == nil || !strings.Contains(e.Error(), "合并") {
		t.Fatal("missing duplicate answer repair feedback", e)
	}
	c.Solution.Answer = c.Solution.Answer[:1]
	c.Solution.Answer[0].Content = "x=1，y=2"
	if e := Validate(c); e != nil {
		t.Fatal("corrected candidate rejected", e)
	}
}
