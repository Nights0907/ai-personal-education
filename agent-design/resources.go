// Package agentdesign loads the editable Markdown prompt and embeds read-only skills.
package agentdesign

import (
	"embed"
	"fmt"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	fsinmem "github.com/musi-ai/education/internal/agent/runtime/afs/inmem"
	"github.com/musi-ai/education/internal/agent/runtime/prompt"
	"gopkg.in/yaml.v3"
	"strings"
)

//go:embed skills/*/SKILL.md contracts/*.json
var Files embed.FS

// SkillMount provides immutable public teaching files. Private student memory
// belongs in a separately authenticated filesystem implementation.
func SkillMount() (afs.FileSystem, map[string]string, error) {
	entries, err := Files.ReadDir("skills")
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	paths := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := "/skills/" + entry.Name() + "/SKILL.md"
		body, e := Files.ReadFile("skills/" + entry.Name() + "/SKILL.md")
		if e != nil {
			return nil, nil, e
		}
		files[path] = body
		paths[entry.Name()] = path
	}
	fs, err := fsinmem.New(files)
	return fs, paths, err
}

func Instruction() (string, error) {
	path, err := prompt.Resolve("")
	if err != nil {
		return "", err
	}
	return prompt.Read(path)
}
func Skills() (map[string]skill.Skill, error) {
	out := map[string]skill.Skill{}
	entries, e := Files.ReadDir("skills")
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		b, e := Files.ReadFile("skills/" + entry.Name() + "/SKILL.md")
		if e != nil {
			return nil, e
		}
		parts := strings.SplitN(strings.ReplaceAll(string(b), "\r\n", "\n"), "---", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" {
			return nil, fmt.Errorf("invalid frontmatter: %s", entry.Name())
		}
		var fm skill.FrontMatter
		if e = yaml.Unmarshal([]byte(parts[1]), &fm); e != nil {
			return nil, e
		}
		if fm.Name != entry.Name() || fm.Description == "" || fm.Context != "" || fm.Model != "" || fm.Agent != "" {
			return nil, fmt.Errorf("unsupported skill: %s", entry.Name())
		}
		out[fm.Name] = skill.Skill{FrontMatter: fm, Content: strings.TrimSpace(parts[2])}
	}
	return out, nil
}
