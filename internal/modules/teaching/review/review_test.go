package review

import (
	"context"
	"testing"

	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
)

type stubRepo struct{}

func (stubRepo) Get(context.Context, string, string) (solve.View, error) { return solve.View{}, nil }

func TestModuleContributeRegistersSkillAndTool(t *testing.T) {
	m := Module{Service: &Service{Repo: stubRepo{}}}
	c, err := m.Contribute(context.Background(), &agentic.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if c.SkillPaths["review-recall"] != "/skills/review-recall/SKILL.md" {
		t.Fatalf("skill path not registered: %+v", c.SkillPaths)
	}
	rt, ok := c.Tools["get_review_question"]
	if !ok {
		t.Fatal("get_review_question tool not registered")
	}
	info, err := rt.Info(context.Background())
	if err != nil || info.Name != "get_review_question" {
		t.Fatalf("tool info invalid: %+v %v", info, err)
	}
	if info.Desc == "" {
		t.Fatal("tool description missing")
	}
}

func TestModuleRequiresRepository(t *testing.T) {
	m := Module{Service: &Service{Repo: nil}}
	if _, err := m.Contribute(context.Background(), &agentic.Turn{}); err == nil {
		t.Fatal("nil repository must be rejected")
	}
}

func TestModuleRegistersImageToolWhenPhotosPresent(t *testing.T) {
	m := Module{Service: &Service{Repo: stubRepo{}, Photos: &photo.Service{}}}
	c, err := m.Contribute(context.Background(), &agentic.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Tools["recognize_review_image"]; !ok {
		t.Fatal("recognize_review_image tool not registered when photos present")
	}
}

func TestModuleOmitsImageToolWithoutPhotos(t *testing.T) {
	m := Module{Service: &Service{Repo: stubRepo{}}}
	c, err := m.Contribute(context.Background(), &agentic.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Tools["recognize_review_image"]; ok {
		t.Fatal("recognize_review_image must be absent when photos nil")
	}
}
