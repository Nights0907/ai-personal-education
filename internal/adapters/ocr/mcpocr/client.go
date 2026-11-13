package mcpocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	URL, Transport, ToolName string
	Configure                map[string]any
}
type Client struct {
	cfg  Config
	http *http.Client
}

func New(c Config) (*Client, error) {
	u, e := url.Parse(c.URL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, photo.ErrUnavailable
	}
	if c.Transport != "sse" && c.Transport != "streamableHttp" {
		return nil, photo.ErrUnavailable
	}
	if c.ToolName == "" {
		c.ToolName = "通用识别GPU"
	}
	if c.Configure == nil {
		c.Configure = map[string]any{"output_prob": true, "output_keypoints": true}
	}
	h := &http.Client{Timeout: 75 * time.Second, Transport: guard{origin: u.Scheme + "://" + u.Host, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OCR redirects disabled") }}
	return &Client{cfg: c, http: h}, nil
}

type guard struct {
	origin string
	base   http.RoundTripper
}

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != g.origin {
		return nil, photo.ErrUnavailable
	}
	res, e := g.base.RoundTrip(r)
	if e != nil {
		return nil, photo.ErrUnavailable
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body = &bounded{ReadCloser: res.Body, left: 8 << 20}
	}
	return res, nil
}

type bounded struct {
	io.ReadCloser
	left int64
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, photo.ErrUnavailable
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, e
}
func (c *Client) ConfigHash() string {
	b, _ := json.Marshal([]any{"aliyun-market-cmapi020020", c.cfg.ToolName, c.cfg.Configure, "gpu-ret-word-v1"})
	return photo.Hash(b)
}
func (c *Client) Recognize(parent context.Context, image []byte) (photo.Output, error) {
	ctx, cancel := context.WithTimeout(parent, 80*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "musi-teacher-ocr", Version: "1.0.0"}, &mcp.ClientOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	var transport mcp.Transport
	if c.cfg.Transport == "sse" {
		transport = &mcp.SSEClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.http, MaxEventSize: 8 << 20}
	} else {
		transport = &mcp.StreamableClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.http, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 8 << 20}
	}
	session, e := client.Connect(ctx, transport, nil)
	if e != nil {
		return photo.Output{}, photo.ErrUnavailable
	}
	defer session.Close()
	tools, e := session.ListTools(ctx, nil)
	if e != nil {
		return photo.Output{}, photo.ErrUnavailable
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == c.cfg.ToolName {
			found = true
		}
	}
	if !found {
		return photo.Output{}, photo.ErrUnavailable
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: c.cfg.ToolName, Arguments: map[string]any{"image": base64.StdEncoding.EncodeToString(image), "configure": c.cfg.Configure}})
	// No transport fallback or automatic retry after tools/call: an interrupted
	// response may already have been billed by the OCR provider.
	if e != nil || result == nil || result.IsError {
		return photo.Output{}, photo.ErrUnavailable
	}
	out, e := Parse(result)
	out.ToolName = c.cfg.ToolName
	out.Transport = c.cfg.Transport
	return out, e
}
func Parse(result *mcp.CallToolResult) (photo.Output, error) {
	candidates := [][]byte{}
	if result.StructuredContent != nil {
		b, _ := json.Marshal(result.StructuredContent)
		candidates = append(candidates, b)
	}
	for _, item := range result.Content {
		if t, ok := item.(*mcp.TextContent); ok {
			candidates = append(candidates, []byte(t.Text))
		}
	}
	for _, raw := range candidates {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var body struct {
			Success   bool   `json:"success"`
			RequestID string `json:"request_id"`
			Ret       []struct {
				Word string `json:"word"`
			} `json:"ret"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		if d.Decode(&body) != nil || !body.Success {
			continue
		}
		parts := []string{}
		for _, v := range body.Ret {
			if strings.TrimSpace(v.Word) != "" {
				parts = append(parts, v.Word)
			}
		}
		text := strings.Join(parts, "\n")
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 20000 {
			continue
		}
		// The market gateway appends documentation after the JSON object. Preserve
		// the original object and coordinates, excluding those non-OCR paragraphs.
		return photo.Output{Text: text, Raw: append([]byte(nil), raw[:d.InputOffset()]...), RequestID: body.RequestID}, nil
	}
	return photo.Output{}, photo.ErrUnavailable
}
