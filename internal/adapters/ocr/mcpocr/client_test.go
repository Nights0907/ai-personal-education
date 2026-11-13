package mcpocr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const sample = `{"success":true,"request_id":"ocr-test","ret":[{"word":"解方程 x+1=2。","keypoints":[1,2],"prob":0.99},{"word":"求x。"}]}  - 以下是返回参数说明，不属于题目`

func TestParseOnlyRecognizedWords(t *testing.T) {
	v, e := Parse(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: sample}}})
	if e != nil || v.Text != "解方程 x+1=2。\n求x。" || !json.Valid(v.Raw) {
		t.Fatal(v, e)
	}
	for _, raw := range []string{`{"success":false,"ret":[{"word":"不能误当成功"}]}`, `{"success":true,"ret":[]}`, `{"success":true,"text":"任意包装不能误解析"}`, `not json`} {
		if _, e := Parse(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: raw}}}); !errors.Is(e, photo.ErrUnavailable) {
			t.Fatal(raw, e)
		}
	}
}
func TestBothMCPTransports(t *testing.T) {
	for _, transport := range []string{"streamableHttp", "sse"} {
		t.Run(transport, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "ocr-test", Version: "1"}, nil)
			var calls atomic.Int32
			server.AddTool(&mcp.Tool{Name: "通用识别GPU", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"image": map[string]any{"type": "string"}, "configure": map[string]any{"type": "object"}}}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				var a struct {
					Image     string         `json:"image"`
					Configure map[string]any `json:"configure"`
				}
				if json.Unmarshal(r.Params.Arguments, &a) != nil {
					return nil, errors.New("invalid params")
				}
				b, e := base64.StdEncoding.DecodeString(a.Image)
				if e != nil || string(b) != "test-image" || a.Configure["output_prob"] != true {
					return nil, errors.New("wrong OCR payload")
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: sample}}}, nil
			})
			var handler http.Handler
			if transport == "sse" {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			} else {
				handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			h := httptest.NewServer(handler)
			defer h.Close()
			c, e := New(Config{URL: h.URL, Transport: transport})
			if e != nil {
				t.Fatal(e)
			}
			v, e := c.Recognize(context.Background(), []byte("test-image"))
			if e != nil || v.RequestID != "ocr-test" || v.Transport != transport || calls.Load() != 1 {
				t.Fatal(v, e, calls.Load())
			}
		})
	}
}
func TestProviderFailureIsNotRetried(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "failed", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "通用识别GPU", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "failure"}}}, nil
	})
	h := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer h.Close()
	c, _ := New(Config{URL: h.URL, Transport: "streamableHttp"})
	_, e := c.Recognize(context.Background(), []byte("x"))
	if !errors.Is(e, photo.ErrUnavailable) || calls.Load() != 1 {
		t.Fatal(e, calls.Load())
	}
}
