package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// IndexTool projects a server-bound, staged L2 document. It is invoked by the
// memory worker, not offered as a write capability to the chatting agent.
// Staging permits L2 and its derived L1 to become visible in one atomic publish.
type IndexTool struct{ summary Document[SummaryContent] }

var _ tool.InvokableTool = (*IndexTool)(nil)

func NewIndexTool(summary Document[SummaryContent]) (*IndexTool, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return &IndexTool{summary: summary}, nil
}

func (t *IndexTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "extract_memory_index", Desc: "从服务端绑定的L2摘要文档提取keywords、路径及版本元数据，生成L1索引。确定性执行，不调用模型，不读取L0/L3，不写存储。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"l2_path": {Type: schema.String, Required: true, Desc: "当前学生已绑定的L2 meta.md路径"},
	})}, nil
}

func (t *IndexTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path string `json:"l2_path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	p, err := MonthlyPaths(t.summary.StudentID, t.summary.Month)
	if err != nil {
		return "", err
	}
	if in.Path != p.Summary {
		return "", errors.New("student memory: L2 index source path not authorized")
	}
	index, err := IndexFromSummary(t.summary)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(index)
	return string(b), err
}

// IndexFromSummary is the shared projection contract for the tool and storage
// validation. There is no input for independently generated index content.
func IndexFromSummary(summary Document[SummaryContent]) (Document[IndexContent], error) {
	if err := summary.Validate(); err != nil {
		return Document[IndexContent]{}, err
	}
	if err := ValidateSummaryContent(summary.Content); err != nil {
		return Document[IndexContent]{}, err
	}
	keywords := []string{}
	for _, area := range summary.Content.KnowledgeAreas {
		for _, t := range area.Topics {
			keywords = append(keywords, area.Area+":"+t.Topic)
		}
	}
	sort.Strings(keywords)
	p, _ := MonthlyPaths(summary.StudentID, summary.Month)
	return Document[IndexContent]{SchemaVersion: summary.SchemaVersion, Layer: "L1", StudentID: summary.StudentID, Month: summary.Month, Revision: summary.Revision, UpdatedAt: summary.UpdatedAt.UTC(), NextPaths: []string{p.Summary}, Content: IndexContent{Keywords: keywords, TurnCount: summary.Content.TurnCount, Span: summary.Content.Span}}, nil
}

func indexThroughTool(ctx context.Context, summary Document[SummaryContent]) (Document[IndexContent], error) {
	t, err := NewIndexTool(summary)
	if err != nil {
		return Document[IndexContent]{}, err
	}
	p, _ := MonthlyPaths(summary.StudentID, summary.Month)
	args, _ := json.Marshal(map[string]string{"l2_path": p.Summary})
	result, err := t.InvokableRun(ctx, string(args))
	if err != nil {
		return Document[IndexContent]{}, err
	}
	var index Document[IndexContent]
	err = json.Unmarshal([]byte(result), &index)
	return index, err
}
