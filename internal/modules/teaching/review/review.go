// Package review implements the recall-review capability: at a forgetting
// threshold, surface the original question and let the student re-derive it
// (Feynman-style self-explanation or photo upload), then judge understanding
// against the stored standard solution. It is read-only with respect to the
// student profile: the student's authored answer flows through the existing
// memory extraction pipeline, which adjudicates mastery server-side.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
)

// Repository is the subset of solve.Repository needed to fetch a committed
// question by ID. It is satisfied by the teaching persistence repository.
type Repository interface {
	Get(context.Context, string, string) (solve.View, error)
}

type Service struct {
	Reviewer model.BaseChatModel
	Queries  query.Repository
	Repo     Repository
	Photos   *photo.Service
}

type Module struct{ Service *Service }

func (m Module) Contribute(ctx context.Context, t *agentic.Turn) (agentic.Contribution, error) {
	if m.Service == nil || m.Service.Repo == nil {
		return agentic.Contribution{}, errors.New("review: repository required")
	}
	tools := map[string]tool.InvokableTool{
		"get_review_question": &reviewTool{service: m.Service},
	}
	if m.Service.Photos != nil {
		tools["recognize_review_image"] = &reviewImageTool{service: m.Service}
	}
	skills := map[string]string{"review-recall": "/skills/review-recall/SKILL.md"}
	var hooks []middleware.LifecycleHooks
	if m.Service.Queries != nil {
		rec, e := m.Service.Queries.Get(ctx, t.Request.Input.Identity.SubjectID, t.Request.Input.InputRef)
		if e != nil {
			return agentic.Contribution{}, e
		}
		if rec.Mode != query.ReviewMode || rec.GuideQuestionID == "" {
			delete(tools, "get_review_question")
			skills = map[string]string{}
		}
		if rec.ImageAssetID == "" || rec.GuideQuestionID == "" {
			delete(tools, "recognize_review_image")
		}
		if rec.GuideQuestionID != "" && m.Service.Reviewer != nil && (rec.Mode == query.GuideMode || rec.Mode == query.ReviewMode) {
			tools["evaluate_student_attempt"] = &evaluationTool{service: m.Service}
			evaluated, loaded, recognized := false, false, rec.ImageAssetID == ""
			path := "/skills/review-recall/SKILL.md"
			if rec.Mode == query.GuideMode {
				path = "/skills/step-tutor/SKILL.md"
			}
			hooks = []middleware.LifecycleHooks{{Name: "teacher.attempt-evidence", Policy: middleware.HookPolicy{Mode: middleware.FailTurn}, BeforeModel: func(_ context.Context, h *middleware.HookInput) error {
				if !evaluated {
					choice := schema.ToolChoiceForced
					h.ToolChoice = &choice
					if !loaded {
						h.ToolAllowlist = []string{"read_file"}
					} else if !recognized {
						h.ToolAllowlist = []string{"recognize_review_image"}
					} else {
						h.ToolAllowlist = []string{"evaluate_student_attempt"}
					}
				}
				return nil
			}, AfterTool: func(_ context.Context, h *middleware.HookInput) error {
				if h.ToolName == "read_file" {
					var a struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(h.Arguments), &a) == nil && a.Path == path {
						loaded = true
					}
				}
				if h.ToolName == "recognize_review_image" {
					recognized = true
				}
				if h.ToolName == "evaluate_student_attempt" {
					evaluated = true
				}
				return nil
			}}}
		}

	}
	return agentic.Contribution{SkillPaths: skills, Tools: tools, LifecycleHooks: hooks}, nil
}

type reviewTool struct{ service *Service }

func (t *reviewTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_review_question",
		Desc: "按题号取回复习任务的原题与标准答案（含解析与步骤）。答案仅供模型对照判定，不得直接展示给学生。只读，不更新档案。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"question_id": {Type: schema.String, Required: true, Desc: "服务端遗忘清单给出的题号"},
		}),
	}, nil
}

func (t *reviewTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	in, ok := runtime.Invocation(ctx)
	if !ok {
		return "", state.ErrConflict
	}
	if err := in.Validate(); err != nil {
		return "", err
	}
	var a struct {
		QuestionID string `json:"question_id"`
	}
	d := json.NewDecoder(strings.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return "", errors.New("review: invalid question_id")
	}
	if a.QuestionID == "" {
		return "", errors.New("review: question_id required")
	}
	if t.service.Queries != nil {
		rec, e := t.service.Queries.Get(ctx, in.Identity.SubjectID, in.InputRef)
		if e != nil {
			return "", e
		}
		if rec.Mode != query.ReviewMode || rec.GuideQuestionID != a.QuestionID {
			return "", state.ErrConflict
		}
	}
	v, err := t.service.Repo.Get(ctx, in.Identity.SubjectID, a.QuestionID)
	if err != nil {
		return "", err
	}
	out := map[string]any{
		"question_id": v.QuestionID,
		"question":    v.Question,
		"solution":    v.Solution,
		"notice":      "原题供学生重新作答；solution 仅供模型对照判定，不得把答案或解析直接展示给学生。",
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// reviewImageTool recognizes a student's photographed solution steps for the
// Feynman check. The asset_id is student-scoped by the photo service itself:
// Recognize loads the asset by (student, id), so cross-student access is
// rejected. It is read-only with respect to the profile.
type reviewImageTool struct{ service *Service }

func (t *reviewImageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "recognize_review_image",
		Desc: "识别学生拍照上传的复习作答图片，返回识别出的文字，供对照标准答案判定。只读，不更新档案。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"asset_id": {Type: schema.String, Required: true, Desc: "学生上传图片的资产编号（对话上下文中给出）"},
		}),
	}, nil
}

func (t *reviewImageTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	in, ok := runtime.Invocation(ctx)
	if !ok {
		return "", state.ErrConflict
	}
	if err := in.Validate(); err != nil {
		return "", err
	}
	if t.service.Photos == nil {
		return "", errors.New("review: OCR unavailable")
	}
	var a struct {
		AssetID string `json:"asset_id"`
	}
	d := json.NewDecoder(strings.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return "", errors.New("review: invalid asset_id")
	}
	if a.AssetID == "" {
		return "", errors.New("review: asset_id required")
	}
	if t.service.Queries != nil {
		rec, e := t.service.Queries.Get(ctx, in.Identity.SubjectID, in.InputRef)
		if e != nil {
			return "", e
		}
		if rec.ImageAssetID != a.AssetID || rec.GuideQuestionID == "" {
			return "", state.ErrConflict
		}
	}
	result, err := t.service.Photos.Recognize(ctx, in.Identity.SubjectID, a.AssetID)
	if err != nil {
		return "", err
	}
	out := map[string]any{
		"asset_id":      result.AssetID,
		"ocr_result_id": result.ResultID,
		"text":          result.Text,
		"notice":        "OCR 仅识别文字，不理解配图；判定仍以学生思路是否自洽为准，不得推断图片中缺失的条件。",
	}
	b, err := json.Marshal(out)
	return string(b), err
}
