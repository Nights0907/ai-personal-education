package context

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"html"
	"regexp"
	"sort"
	"strings"
)

type Slot int

const (
	SlotPreamble Slot = iota
	SlotMemory
	SlotUserPrefix
)
const (
	PrioritySkills          = 100
	PriorityLongTermMemory  = 100
	PriorityShortTermMemory = 900
	PriorityTail            = 1000
)

type Block struct {
	Tag     string
	Attrs   map[string]string
	Content string
	Markup  bool
}
type BuildInput struct {
	Input   state.Input
	Binding state.Binding
	View    state.View
}
type StaticBlockProvider struct {
	Name     string
	Priority int
	Provide  func() []Block
}
type TurnProvider struct {
	Name     string
	Slot     Slot
	Priority int
	Provide  func(context.Context, BuildInput) ([]Block, error)
}
type Assembled struct {
	Messages         []*schema.Message
	Preamble, Memory string
	UsedCompact      state.Summary
}

var tagPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// Markup is restricted to server-created nested blocks, never raw student data.
func RenderBlocks(blocks []Block) (string, error) {
	var out []string
	for _, b := range blocks {
		if !tagPattern.MatchString(b.Tag) {
			return "", errors.New("context: invalid block tag")
		}
		if b.Content == "" && len(b.Attrs) == 0 {
			continue
		}
		s := "<" + b.Tag
		keys := []string{}
		for k := range b.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !tagPattern.MatchString(k) {
				return "", errors.New("context: invalid attribute")
			}
			s += " " + k + "=\"" + html.EscapeString(b.Attrs[k]) + "\""
		}
		if b.Content == "" {
			out = append(out, s+" />")
			continue
		}
		content := b.Content
		if !b.Markup {
			content = html.EscapeString(content)
		}
		out = append(out, s+">\n"+content+"\n</"+b.Tag+">")
	}
	return strings.Join(out, "\n"), nil
}
func AssembleSystemPrompt(base string, providers ...StaticBlockProvider) (string, error) {
	ps := append([]StaticBlockProvider(nil), providers...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Priority < ps[j].Priority })
	out := []string{base}
	for _, p := range ps {
		if p.Provide == nil {
			return "", fmt.Errorf("context: missing static provider %s", p.Name)
		}
		s, e := RenderBlocks(p.Provide())
		if e != nil {
			return "", e
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n"), nil
}
func Assemble(ctx context.Context, in BuildInput, providers []TurnProvider, history []*schema.Message, user *schema.Message) (Assembled, error) {
	ps := append([]TurnProvider(nil), providers...)
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Slot != ps[j].Slot {
			return ps[i].Slot < ps[j].Slot
		}
		return ps[i].Priority < ps[j].Priority
	})
	slots := [3][]Block{}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name == "" || seen[p.Name] || p.Slot < SlotPreamble || p.Slot > SlotUserPrefix || p.Provide == nil {
			return Assembled{}, errors.New("context: invalid or duplicate turn provider")
		}
		seen[p.Name] = true
		b, e := p.Provide(ctx, in)
		if e != nil {
			return Assembled{}, fmt.Errorf("provider %s: %w", p.Name, e)
		}
		slots[p.Slot] = append(slots[p.Slot], b...)
	}
	var a Assembled
	var e error
	a.Preamble, e = RenderBlocks(slots[SlotPreamble])
	if e != nil {
		return a, e
	}
	a.Memory, e = RenderBlocks(slots[SlotMemory])
	if e != nil {
		return a, e
	}
	prefix, e := RenderBlocks(slots[SlotUserPrefix])
	if e != nil {
		return a, e
	}
	if a.Preamble != "" {
		a.Messages = append(a.Messages, schema.UserMessage(a.Preamble))
	}
	if a.Memory != "" {
		a.Messages = append(a.Messages, schema.UserMessage(a.Memory))
	}
	a.Messages = append(a.Messages, state.CleanMessages(history)...)
	if user == nil {
		return a, errors.New("context: missing user message")
	}
	m := state.CleanMessages([]*schema.Message{user})[0]
	if prefix != "" {
		m.Content = prefix + "\n\n" + m.Content
	}
	a.Messages = append(a.Messages, m)
	a.UsedCompact = in.View.Summary
	return a, nil
}
