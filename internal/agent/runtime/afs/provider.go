package afs

import (
	"context"
	"errors"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
)

type FileRef struct {
	Path, Name, Content string
	PreLoad             bool
}

// FilesProvider supports mounted references, preloaded files, and inline labels.
// An inline value bearing Path must still refer to a genuinely readable file.
func FilesProvider(name, tag string, slot ctxbuild.Slot, priority int, fs FileSystem, refs []FileRef) ctxbuild.TurnProvider {
	refs = append([]FileRef(nil), refs...)
	return ctxbuild.TurnProvider{Name: name, Slot: slot, Priority: priority, Provide: func(ctx context.Context, _ ctxbuild.BuildInput) ([]ctxbuild.Block, error) {
		blocks := []ctxbuild.Block{}
		for _, ref := range refs {
			attrs := map[string]string{}
			content := ref.Content
			if ref.Path != "" {
				if fs == nil {
					return nil, errors.New("afs: mounted file requires filesystem")
				}
				p, e := Clean(ref.Path)
				if e != nil {
					return nil, e
				}
				attrs["path"] = p
				if ref.PreLoad || content != "" {
					b, e := fs.Read(ctx, p)
					if e != nil {
						return nil, e
					}
					if len(b) > MaxReadBytes {
						return nil, errors.New("afs: file too large")
					}
					if content == "" {
						content = string(b)
					}
				}
			} else if ref.Name != "" {
				attrs["name"] = ref.Name
			} else {
				return nil, errors.New("afs: file path or inline name required")
			}
			blocks = append(blocks, ctxbuild.Block{Tag: "file", Attrs: attrs, Content: content})
		}
		rendered, e := ctxbuild.RenderBlocks(blocks)
		if e != nil {
			return nil, e
		}
		return []ctxbuild.Block{{Tag: tag, Content: rendered, Markup: true}}, nil
	}}
}
