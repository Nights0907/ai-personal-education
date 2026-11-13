// Package skill exposes SKILL.md metadata as a stable context provider. Bodies
// are loaded by the ordinary read_file tool; there is no dedicated skill tool.
package skill

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"gopkg.in/yaml.v3"
	"strings"
)

type Ref struct{ Path string }
type Catalog struct{ Items []state.SkillDescriptor }

func NewCatalog(ctx context.Context, fs afs.FileSystem, refs []Ref) (*Catalog, error) {
	if fs == nil {
		return nil, errors.New("skill: filesystem required")
	}
	c := &Catalog{}
	seen := map[string]bool{}
	for _, ref := range refs {
		p, e := afs.Clean(ref.Path)
		if e != nil {
			return nil, e
		}
		b, e := fs.Read(ctx, p)
		if e != nil {
			return nil, e
		}
		parts := strings.SplitN(strings.ReplaceAll(string(b), "\r\n", "\n"), "---", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" {
			return nil, errors.New("skill: missing YAML frontmatter")
		}
		var fm struct {
			Name                  string `yaml:"name"`
			Description           string `yaml:"description"`
			Context, Model, Agent string
		}
		if e = yaml.Unmarshal([]byte(parts[1]), &fm); e != nil {
			return nil, e
		}
		if fm.Name == "" || fm.Description == "" || seen[fm.Name] || fm.Context != "" || fm.Model != "" || fm.Agent != "" {
			return nil, errors.New("skill: duplicate or unsupported skill metadata")
		}
		seen[fm.Name] = true
		c.Items = append(c.Items, state.SkillDescriptor{Name: fm.Name, Description: fm.Description, Path: p})
	}
	return c, nil
}
func (c *Catalog) Provider() ctxbuild.TurnProvider {
	return ctxbuild.TurnProvider{Name: "available-skills", Slot: ctxbuild.SlotPreamble, Priority: 100, Provide: func(context.Context, ctxbuild.BuildInput) ([]ctxbuild.Block, error) {
		var content strings.Builder
		for _, s := range c.Items {
			v, e := ctxbuild.RenderBlocks([]ctxbuild.Block{{Tag: "skill", Attrs: map[string]string{"name": s.Name, "path": s.Path}, Content: s.Description}})
			if e != nil {
				return nil, e
			}
			content.WriteString(v)
			content.WriteByte('\n')
		}
		return []ctxbuild.Block{{Tag: "available-skills", Content: content.String(), Markup: true}}, nil
	}}
}

func NewCatalogProvider(ctx context.Context, fs afs.FileSystem, refs []Ref) (ctxbuild.TurnProvider, error) {
	c, e := NewCatalog(ctx, fs, refs)
	if e != nil {
		return ctxbuild.TurnProvider{}, e
	}
	return c.Provider(), nil
}
