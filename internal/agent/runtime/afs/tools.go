package afs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
)

type fileTool struct {
	fs   FileSystem
	name string
}

func Tools(fs FileSystem) []tool.BaseTool {
	return []tool.BaseTool{&fileTool{fs: fs, name: "read_file"}, &fileTool{fs: fs, name: "ls"}}
}
func (t *fileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "Read an authorized virtual file or list a mounted directory. Use absolute skill paths from the supplied skill catalog. Student memory paths are not mounted files: use read_student_memory for L2/L3, not read_file.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true, Desc: "Absolute virtual path"}})}, nil
}
func (t *fileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	d := json.NewDecoder(strings.NewReader(args))
	d.DisallowUnknownFields()
	if e := d.Decode(&in); e != nil {
		return "", e
	}
	if _, e := Clean(in.Path); e != nil {
		return "", e
	}
	if t.fs == nil {
		return "", errors.New("afs: filesystem unavailable")
	}
	if t.name == "read_file" {
		b, e := t.fs.Read(ctx, in.Path)
		if len(b) > MaxReadBytes {
			return "", errors.New("afs: file too large")
		}
		if e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			result, _ := json.Marshal(map[string]any{"ok": false, "error": "file_unavailable", "message": "File is not mounted or not authorized. Choose an exact skill path from available-skills. Student memory must be read with read_student_memory when available; do not read memory paths with read_file."})
			return string(result), nil
		}
		return string(b), nil
	}
	entries, e := t.fs.List(ctx, in.Path)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(entries)
	if len(b) > MaxReadBytes {
		return "", errors.New("afs: directory listing too large")
	}
	return string(b), e
}
