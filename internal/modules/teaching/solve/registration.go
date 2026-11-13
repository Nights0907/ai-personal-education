package solve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	agentdesign "github.com/musi-ai/education/agent-design"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	runtime "github.com/musi-ai/education/internal/agent/runtime"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/query"
	"sort"
	"strings"
	"unicode/utf8"
)

type Module struct{ Service *Service }

func (m Module) Contribute(ctx context.Context, t *agentic.Turn) (agentic.Contribution, error) {
	src, e := m.Service.Input(ctx, t.Request.Input)
	if e != nil {
		return agentic.Contribution{}, e
	}
	if src.GuideQuestionID != "" || src.Mode == query.ReviewMode {
		return agentic.Contribution{}, nil
	}
	infos, e := toolInfos()
	if e != nil {
		return agentic.Contribution{}, e
	}
	infos["submit_solution"] = &schema.ToolInfo{Name: "submit_solution", Desc: "提交完整候选解答，内部完成格式校验和事务保存；失败按issues修改后重试，成功返回正式回执。", ParamsOneOf: infos["validate_solution"].ParamsOneOf}
	tools := map[string]tool.InvokableTool{}
	if src.ImageAssetID != "" && m.Service.Photos != nil {
		tools["recognize_input_image"] = &solveTool{name: "recognize_input_image", info: infos["recognize_input_image"], service: m.Service}
	}
	if src.IsGuide() && openingPublisher(ctx) != nil {
		infos["publish_guiding_question"] = &schema.ToolInfo{Name: "publish_guiding_question", Desc: "在后台准备参考解答前，先发布一个只针对题目已知条件、目标或方法选择的起步问题；不得包含计算结果、答案或完整解法。发布后继续校验并保存参考答案，不等待学生回复。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"question": {Type: schema.String, Required: true, Desc: "一个简短问题，不超过300字，不含解答结果"}})}
		tools["publish_guiding_question"] = &solveTool{name: "publish_guiding_question", info: infos["publish_guiding_question"], service: m.Service}
	}
	radarVerified := false
	if src.RadarSourceID != "" {
		infos["submit_solution"] = &schema.ToolInfo{Name: "submit_solution", Desc: "提交本轮已严格校验的题目雷达变式。仅传空对象{}；服务端原样保存verify_question_variant校验的候选，不需要模型重写或复制整份JSON。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}
		for _, name := range []string{"generate_question_variant", "verify_question_variant"} {
			info := &schema.ToolInfo{Name: name, Desc: map[string]string{"generate_question_variant": "基于本轮绑定原题换数值生成可精确验算的变式；不修改来源，不猜条件。", "verify_question_variant": "独立验算本轮变式数值、可解性与完整参考解答，返回严格校验通过的candidate；必须原样提交。"}[name], ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}
			tools[name] = &solveTool{name: name, info: info, service: m.Service}
		}
	}
	openingSent := false
	inputRecognized := src.ImageAssetID == ""
	loaded := false
	emit := func(c context.Context, p agentic.Packet) error {
		p.BizName = "math_solve"
		p.TurnID = t.Request.Input.Identity.TurnID
		p.MessageID = t.AssistantMessageID
		return t.Sink.Send(c, p)
	}
	for _, name := range []string{"get_task_input", "get_knowledge_catalog", "validate_solution", "finalize_solution", "submit_solution"} {
		tools[name] = &solveTool{name: name, info: infos[name], service: m.Service}
	}
	skillPaths := map[string]string{"math-solve": "/skills/math-solve/SKILL.md"}
	if src.RadarSourceID != "" {
		skillPaths["question-variant"] = "/skills/question-variant/SKILL.md"
		skillPaths["question-verification"] = "/skills/question-verification/SKILL.md"
	}
	return agentic.Contribution{Tools: tools, SkillPaths: skillPaths,
		TurnProviders: []ctxbuild.TurnProvider{{Name: "solve-source", Slot: ctxbuild.SlotUserPrefix, Priority: 100, Provide: func(context.Context, ctxbuild.BuildInput) ([]ctxbuild.Block, error) {
			kind := "text"
			if src.ImageAssetID != "" {
				kind = "image"
			}
			mode := "SOLVE"
			if src.IsGuide() {
				mode = "GUIDE"
			}
			problem := map[string]any{"mode": mode, "input_type": kind, "knowledge_catalog": Catalog, "question_text": src.Query, "answer_delivery": map[bool]string{true: "hidden_until_student_completes", false: "show_after_commit"}[src.IsGuide()]}
			if src.RadarSourceID != "" {
				problem["radar"] = map[string]any{"source_question_id": src.RadarSourceID, "instruction": "先加载question-variant与question-verification，调用generate_question_variant及verify_question_variant，调用submit_solution({})提交服务端已验算候选，不复制候选JSON。必须完成严格校验；不再求解用户请求中的旧题。"}
			}
			if src.ImageAssetID != "" {
				problem["image_asset_id"] = src.ImageAssetID
			}
			b, _ := json.Marshal(problem)
			return []ctxbuild.Block{{Tag: "current-problem", Content: string(b)}}, nil
		}}},
		LifecycleHooks: []middleware.LifecycleHooks{{Name: "solve.authorization", Policy: middleware.HookPolicy{Mode: middleware.FailTurn}, BeforeModel: func(c context.Context, h *middleware.HookInput) error {
			choice := schema.ToolChoiceForced
			h.ToolChoice = &choice
			// The model chooses the content; phase permissions avoid redundant lookups.
			if !loaded {
				h.ToolAllowlist = []string{"read_file", "read_student_memory", "list_student_memory_indexes"}
			} else if !inputRecognized {
				h.ToolAllowlist = []string{"recognize_input_image"}
			} else if !src.IsGuide() || openingPublisher(c) == nil || openingSent {
				h.ToolAllowlist = []string{"submit_solution", "read_file", "read_student_memory", "list_student_memory_indexes"}
			}

			if src.IsGuide() && openingPublisher(c) != nil && !openingSent {
				h.ToolAllowlist = []string{"read_file", "read_student_memory", "list_student_memory_indexes"}
				if loaded && !inputRecognized {
					h.ToolAllowlist = []string{"recognize_input_image"}
				}
				if loaded && inputRecognized {
					h.ToolAllowlist = []string{"publish_guiding_question"}
				}
			}
			if src.RadarSourceID != "" && loaded {
				h.ToolAllowlist = []string{"read_file", "generate_question_variant", "verify_question_variant", "submit_solution"}
			}
			return emit(c, agentic.Packet{Type: "progress", Stage: "generating", Text: "正在生成解题内容…"})
		}, BeforeTool: func(c context.Context, h *middleware.HookInput) (middleware.ToolDecision, error) {
			in, ok := runtime.Invocation(c)
			if !ok {
				return middleware.Deny, state.ErrConflict
			}
			if e := in.Validate(); e != nil {
				return middleware.Deny, e
			}
			if _, e := m.Service.Input(c, in); e != nil {
				return middleware.Deny, e
			}
			if h.ToolName == "publish_guiding_question" && (!loaded || !inputRecognized) {
				return middleware.Deny, errors.New("solve: a reliable question is required before opening")
			}
			if (h.ToolName == "validate_solution" || h.ToolName == "submit_solution") && src.IsGuide() && openingPublisher(c) != nil && !openingSent {
				return middleware.Deny, fmt.Errorf("solve: publish the opening question before preparing the reference")
			}
			if (h.ToolName == "validate_solution" || h.ToolName == "submit_solution") && !loaded {
				return middleware.Deny, fmt.Errorf("solve: read math-solve skill before validation")
			}
			if src.RadarSourceID != "" && h.ToolName == "submit_solution" && !radarVerified {
				return middleware.Deny, errors.New("radar: verify_question_variant必须成功后才能提交")
			}
			stage, text := "", ""
			switch h.ToolName {
			case "read_file":
				stage, text = "preparing", "正在准备解题…"
			case "recognize_input_image":
				stage, text = "recognizing", "正在识别题目图片…"
			case "validate_solution", "submit_solution":
				stage, text = "validating", "AI教师正在整理讲解，请稍候~"
				if v := Preview(h.Arguments); v != nil {
					if e := emit(c, agentic.Packet{Type: "draft", Data: v}); e != nil {
						return middleware.Deny, e
					}
				}
			case "finalize_solution":
				stage, text = "saving", "讲解已完成，正在为你保存~"
			}
			if stage != "" {
				if e := emit(c, agentic.Packet{Type: "progress", Stage: stage, Text: text}); e != nil {
					return middleware.Deny, e
				}
			}
			return middleware.Allow, nil
		}, AfterTool: func(c context.Context, h *middleware.HookInput) error {
			if h.ToolName == "verify_question_variant" {
				var result struct {
					Verified bool `json:"verified"`
				}
				if json.Unmarshal([]byte(h.Result), &result) == nil && result.Verified {
					radarVerified = true
				}
			}
			if h.ToolName == "publish_guiding_question" {
				openingSent = true
			}
			if h.ToolName == "read_file" {
				var a struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(h.Arguments), &a) == nil && a.Path == "/skills/math-solve/SKILL.md" {
					loaded = true
				}
			}
			if h.ToolName == "recognize_input_image" {
				inputRecognized = true
				var v struct {
					Text string `json:"question_text"`
				}
				if json.Unmarshal([]byte(h.Result), &v) == nil {
					return emit(c, agentic.Packet{Type: "ocr", Text: v.Text})
				}
			}
			if h.ToolName == "validate_solution" {
				var v struct {
					Passed bool     `json:"passed"`
					Issues []string `json:"issues"`
				}
				if json.Unmarshal([]byte(h.Result), &v) == nil {
					return emit(c, agentic.Packet{Type: "validation", Data: v})
				}
			}
			return nil
		}}},
		PostRuntimeHooks: []func(context.Context, *agentic.Turn) error{func(c context.Context, _ *agentic.Turn) error {
			saved, err := m.Service.Repo.Find(c, t.Request.Input.Identity.Scope, t.Request.Input.Identity.TurnID)
			if err == state.ErrNotFound {
				return nil
			}
			if err != nil {
				return err
			}
			return m.Service.Repair(c, saved)
		}},
	}, nil
}

type solveTool struct {
	name        string
	info        *schema.ToolInfo
	service     *Service
	validations int
}

func (t *solveTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }
func (t *solveTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	in, ok := runtime.Invocation(ctx)
	if !ok {
		return "", state.ErrConflict
	}
	if e := in.Validate(); e != nil {
		return "", e
	}
	src, e := t.service.Input(ctx, in)
	if e != nil {
		return "", e
	}
	var out any
	switch t.name {
	case "publish_guiding_question":
		var a struct {
			Question string `json:"question"`
		}
		if e = Decode(args, &a); e != nil {
			return "", e
		}
		if !src.IsGuide() || openingPublisher(ctx) == nil || strings.TrimSpace(a.Question) == "" || utf8.RuneCountInString(a.Question) > 300 {
			return "", state.ErrConflict
		}
		for i, r := range a.Question {
			if r == '？' || r == '?' {
				a.Question = a.Question[:i+len(string(r))]
				break
			}
		}
		text, err := openingPublisher(ctx)(ctx, a.Question)
		if err != nil {
			return "", err
		}
		out = map[string]any{"question": text, "notice": "第一问已发布。现在继续生成、校验并提交后台参考解答，不要等待学生输入，不要再发布第一问。"}
	case "recognize_input_image":
		var a struct {
			AssetID string `json:"asset_id"`
		}
		if e = Decode(args, &a); e == nil {
			if a.AssetID == "" || a.AssetID != src.ImageAssetID || t.service.Photos == nil {
				return "", state.ErrConflict
			}
			result, err := t.service.Photos.Recognize(ctx, in.Identity.SubjectID, a.AssetID)
			e = err
			if e == nil {
				e = photo.ValidateQuestionText(result.Text)
			}
			if e == nil {
				out = map[string]any{"asset_id": result.AssetID, "ocr_result_id": result.ResultID, "question_text": result.Text, "text_hash": result.TextHash, "notice": "OCR仅识别文字，不理解配图；不要推断缺失条件。"}
			}
		}
	case "get_task_input":
		var a struct{}
		if e = Decode(args, &a); e == nil {
			out = map[string]any{"mode": "SOLVE", "input_type": "text", "question_text": src.Query, "attempt_text": nil, "source_version": SourceHash(src), "assets": []any{}, "outline_version": "pilot-math-topics-v1", "knowledge_catalog_usage": "首次调用get_knowledge_catalog请使用topic_codes=[]，不要猜测代码"}
			if src.ImageAssetID != "" {
				v := out.(map[string]any)
				v["input_type"] = "image"
				v["assets"] = []any{map[string]any{"asset_id": src.ImageAssetID, "purpose": "question"}}
				v["ocr_instruction"] = "调用recognize_input_image，以识别原文为解题依据；question_text只是学生备注。不得补全配图含义。"
			}
		}
	case "get_knowledge_catalog":
		var a struct {
			TopicCodes []string `json:"topic_codes"`
		}
		if e = Decode(args, &a); e == nil {
			codes := make([]string, 0, len(Catalog))
			for code := range Catalog {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			nodes := []map[string]any{}
			for _, code := range codes {
				nodes = append(nodes, map[string]any{"code": code, "name": Catalog[code], "is_core": true})
			}
			out = map[string]any{"outline_version": "pilot-math-topics-v1", "available_topic_codes": codes, "knowledge_nodes": nodes, "note": "试点统一主题级目录，使用返回的code；topic_codes=[]读取全部目录"}
			for _, code := range a.TopicCodes {
				if _, ok := Catalog[code]; !ok {
					out.(map[string]any)["error_code"] = "UNKNOWN_CODE"
					out.(map[string]any)["message"] = "使用返回目录中的有效编码；不要猜测知识代码。"
				}
			}
		}
	case "generate_question_variant", "verify_question_variant":
		if src.RadarSourceID == "" {
			return "", state.ErrConflict
		}
		var empty struct{}
		if e = Decode(args, &empty); e != nil {
			return "", e
		}
		_, err := t.service.Repo.Get(ctx, in.Identity.SubjectID, src.RadarSourceID)
		if err != nil {
			return "", err
		}
		candidate, err := t.service.RadarCandidate(ctx, in.Identity.SubjectID, src.RadarSourceID, src.RadarSeed, t.name == "verify_question_variant")
		if err != nil {
			return "", err
		}
		if t.name == "verify_question_variant" {
			if err = t.service.CheckRadar(ctx, in.Identity.SubjectID, src.RadarSourceID, src.RadarSeed, candidate); err != nil {
				return "", err
			}
		}
		out = map[string]any{"candidate": candidate, "verified": t.name == "verify_question_variant", "checks": []string{"来源归属", "新数值范围", "存在确定解", "独立计算与代回", "答案与步骤一致"}, "source_question_id": src.RadarSourceID}
	case "validate_solution", "submit_solution":
		t.validations++
		if t.validations > 3 {
			return "", fmt.Errorf("solve: validation retry limit")
		}
		var a struct {
			Candidate Candidate `json:"candidate"`
		}
		if src.RadarSourceID != "" {
			var empty struct{}
			if e = Decode(args, &empty); e != nil {
				return "", e
			}
			_, err := t.service.Repo.Get(ctx, in.Identity.SubjectID, src.RadarSourceID)
			if err != nil {
				return "", err
			}
			a.Candidate, e = t.service.RadarCandidate(ctx, in.Identity.SubjectID, src.RadarSourceID, src.RadarSeed, true)
		} else {
			e = Decode(args, &a)
		}
		if e == nil {
			out, e = t.service.ValidateDraft(ctx, in, a.Candidate)
			if result, ok := out.(map[string]any); ok && result["passed"] == false {
				result["knowledge_catalog"] = Catalog
				result["repair_instructions"] = "不要把引导首问或教学步骤创建为原题小问。原题未分小问时question.subquestions=[]，所有answer和steps的subquestion_id=null；原题确有小问时每项引用其已声明ID，不能混用null。knowledge_points.code只能使用返回目录中的键。保持原题数学条件不变。"
			}
		} else {
			out = map[string]any{"passed": false, "draft_id": nil, "issues": []string{"候选参数不符合工具Schema，请修正字段与JSON结构：" + e.Error()}, "review_status": "structure_failed"}
			e = nil
		}
		if t.name == "submit_solution" && e == nil {
			v, ok := out.(map[string]any)
			if ok && v["passed"] == true {
				call, ok := runtime.CurrentTool(ctx)
				if !ok {
					return "", state.ErrProtocol
				}
				prefix, ok := runtime.RecoveryPrefix(ctx)
				if !ok {
					return "", state.ErrProtocol
				}
				saved, err := t.service.Repo.Commit(ctx, in, v["draft_id"].(string), t.service.ModelRef, state.TerminalObservation{CallID: call.CallID, ToolName: call.Name, Arguments: call.Arguments, ArgumentsHash: call.ArgumentsHash}, prefix)
				if err != nil {
					return "", err
				}
				return saved.Receipt.Terminal.Result, nil
			}
		}
	case "finalize_solution":
		var a struct {
			DraftID string `json:"draft_id"`
		}
		if e = Decode(args, &a); e == nil {
			call, ok := runtime.CurrentTool(ctx)
			if !ok {
				return "", state.ErrProtocol
			}
			prefix, ok := runtime.RecoveryPrefix(ctx)
			if !ok {
				return "", state.ErrProtocol
			}
			saved, err := t.service.Repo.Commit(ctx, in, a.DraftID, t.service.ModelRef, state.TerminalObservation{CallID: call.CallID, ToolName: call.Name, Arguments: call.Arguments, ArgumentsHash: call.ArgumentsHash}, prefix)
			if err != nil {
				return "", err
			}
			return saved.Receipt.Terminal.Result, nil
		}
	default:
		return "", state.ErrConflict
	}
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(out)
	return string(b), e
}
func toolInfos() (map[string]*schema.ToolInfo, error) {
	b, e := agentdesign.Files.ReadFile("contracts/tools.json")
	if e != nil {
		return nil, e
	}
	var catalog struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		}
		Defs map[string]any `json:"$defs"`
	}
	if e = json.Unmarshal(b, &catalog); e != nil {
		return nil, e
	}
	var expand func(any) (any, error)
	expand = func(v any) (any, error) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				item, exists := catalog.Defs[strings.TrimPrefix(ref, "#/$defs/")]
				if !exists {
					return nil, fmt.Errorf("solve: unknown schema ref")
				}
				return expand(item)
			}
			out := map[string]any{}
			for k, item := range x {
				v, e := expand(item)
				if e != nil {
					return nil, e
				}
				out[k] = v
			}
			return out, nil
		case []any:
			out := []any{}
			for _, item := range x {
				v, e := expand(item)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			return out, nil
		default:
			return v, nil
		}
	}
	out := map[string]*schema.ToolInfo{}
	for _, entry := range catalog.Tools {
		v, e := expand(entry.Parameters)
		if e != nil {
			return nil, e
		}
		b, _ := json.Marshal(v)
		var params jsonschema.Schema
		if e = json.Unmarshal(b, &params); e != nil {
			return nil, e
		}
		out[entry.Name] = &schema.ToolInfo{Name: entry.Name, Desc: entry.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)}
	}
	return out, nil
}
