package studentmemory

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/redis/go-redis/v9"
)

const MaxDocumentBytes = 8 << 20

type Documents struct{ Client *redis.Client }

var _ mem.Documents = (*Documents)(nil)

func (d *Documents) Load(ctx context.Context, student, month string) (mem.Month, error) {
	p, err := mem.MonthlyPaths(student, month)
	if err != nil {
		return mem.Month{}, err
	}
	values, err := d.Client.MGet(ctx, p.Index, p.Summary, p.Content).Result()
	if err != nil {
		return mem.Month{}, err
	}
	m := mem.Month{}
	absent := 0
	for i, v := range values {
		if v == nil {
			absent++
			continue
		}
		text, ok := v.(string)
		if !ok || len(text) > MaxDocumentBytes {
			return m, errors.New("student memory: invalid Redis document")
		}
		m.Encoded[i] = text
	}
	if absent == 3 {
		return m, mem.ErrNotFound
	}
	if absent != 0 {
		return m, errors.New("student memory: incomplete month; recovery required")
	}
	if m.Index, err = mem.Decode[mem.IndexContent]([]byte(m.Encoded[0]), MaxDocumentBytes); err != nil {
		return m, err
	}
	if m.Summary, err = mem.Decode[mem.SummaryContent]([]byte(m.Encoded[1]), MaxDocumentBytes); err != nil {
		return m, err
	}
	if m.Raw, err = mem.Decode[mem.RawContent]([]byte(m.Encoded[2]), MaxDocumentBytes); err != nil {
		return m, err
	}
	if m.Index.StudentID != student || m.Summary.StudentID != student || m.Raw.StudentID != student || m.Index.Month != month || m.Summary.Month != month || m.Raw.Month != month || m.Index.Revision != m.Raw.Revision || m.Summary.Revision != m.Raw.Revision {
		return m, errors.New("student memory: inconsistent month identity or revision")
	}
	return m, nil
}

// All three Redis strings are replaced atomically, with no TTL. Compare the
// exact old values, not YAML parsed by Redis, to reject stale worker writes.
var publish = redis.NewScript(`
for i=1,3 do
 local old=redis.call('GET',KEYS[i]) or ''
 if old~=ARGV[i] then return 0 end
end
for i=1,3 do redis.call('SET',KEYS[i],ARGV[i+3]) end
return 1`)

func (d *Documents) Publish(ctx context.Context, student, month string, old, next mem.Month) error {
	p, err := mem.MonthlyPaths(student, month)
	if err != nil {
		return err
	}
	if next.Index.StudentID != student || next.Summary.StudentID != student || next.Raw.StudentID != student || next.Index.Month != month || next.Summary.Month != month || next.Raw.Month != month || next.Index.Revision != old.Raw.Revision+1 || next.Summary.Revision != next.Index.Revision || next.Raw.Revision != next.Index.Revision {
		return mem.ErrConflict
	}
	expected, err := mem.IndexFromSummary(next.Summary)
	if err != nil {
		return err
	}
	if next.Index.Content.Navigation != nil {
		if err := mem.ValidateNavigation(*next.Index.Content.Navigation, next.Summary); err != nil {
			return err
		}
		expected.Content.Navigation = next.Index.Content.Navigation
	}
	if !reflect.DeepEqual(next.Index, expected) {
		return errors.New("student memory: L1 must be derived exclusively from L2")
	}
	if len(next.Raw.Content.Turns) < len(old.Raw.Content.Turns) {
		return errors.New("student memory: raw history deletion denied")
	}
	for i, turn := range old.Raw.Content.Turns {
		if !reflect.DeepEqual(turn, next.Raw.Content.Turns[i]) {
			return errors.New("student memory: raw history rewrite denied")
		}
	}
	seen := map[string]bool{}
	for i, turn := range next.Raw.Content.Turns {
		key := mem.SourceID(turn.Source)
		if seen[key] {
			return errors.New("student memory: duplicate raw source")
		}
		seen[key] = true
		if turn.Seq != i+1 {
			return errors.New("student memory: raw seq not contiguous")
		}
	}
	i, err := mem.Encode(next.Index, MaxDocumentBytes)
	if err != nil {
		return err
	}
	s, err := mem.Encode(next.Summary, MaxDocumentBytes)
	if err != nil {
		return err
	}
	r, err := mem.Encode(next.Raw, MaxDocumentBytes)
	if err != nil {
		return err
	}
	n, err := publish.Run(ctx, d.Client, []string{p.Index, p.Summary, p.Content}, old.Encoded[0], old.Encoded[1], old.Encoded[2], string(i), string(s), string(r)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return mem.ErrConflict
	}
	return nil
}

func (d *Documents) Indexes(ctx context.Context, student string, limit int) ([]mem.Document[mem.IndexContent], error) {
	if _, err := mem.MonthlyPaths(student, "2026-01"); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 120 {
		return nil, errors.New("student memory: invalid index limit")
	}
	paths := []string{}
	var cursor uint64
	for {
		keys, next, err := d.Client.Scan(ctx, cursor, "/user/memory/indexes/"+student+"/*.md", 100).Result()
		if err != nil {
			return nil, err
		}
		paths = append(paths, keys...)
		cursor = next
		if len(paths) > 1200 {
			return nil, errors.New("student memory: index capacity exceeded")
		}
		if cursor == 0 {
			break
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	out := []mem.Document[mem.IndexContent]{}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		month := strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".md")
		expected, err := mem.MonthlyPaths(student, month)
		if err != nil || expected.Index != p {
			return nil, errors.New("student memory: invalid index path")
		}
		b, err := d.Client.Get(ctx, p).Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		index, err := mem.Decode[mem.IndexContent](b, MaxDocumentBytes)
		if err != nil {
			return nil, err
		}
		if index.StudentID != student || index.Month != month || index.Layer != "L1" {
			return nil, errors.New("student memory: invalid index owner")
		}
		out = append(out, index)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// LoadSummary reads only L2; current-state scans never fetch raw L3 transcripts.
func (d *Documents) LoadSummary(ctx context.Context, student, month string) (mem.Document[mem.SummaryContent], error) {
	p, e := mem.MonthlyPaths(student, month)
	if e != nil {
		return mem.Document[mem.SummaryContent]{}, e
	}
	raw, e := d.Client.Get(ctx, p.Summary).Bytes()
	if errors.Is(e, redis.Nil) {
		return mem.Document[mem.SummaryContent]{}, mem.ErrNotFound
	}
	if e != nil {
		return mem.Document[mem.SummaryContent]{}, e
	}
	v, e := mem.Decode[mem.SummaryContent](raw, MaxDocumentBytes)
	if e == nil && (v.StudentID != student || v.Month != month || v.Layer != "L2") {
		e = mem.ErrConflict
	}
	return v, e
}

// validatedMonth applies the same invariants before the durable database CAS.
func validatedMonth(student, month string, old, next mem.Month) (mem.Month, error) {
	_, err := mem.MonthlyPaths(student, month)
	if err != nil {
		return mem.Month{}, err
	}
	if next.Index.StudentID != student || next.Summary.StudentID != student || next.Raw.StudentID != student || next.Index.Month != month || next.Summary.Month != month || next.Raw.Month != month || next.Index.Revision != old.Raw.Revision+1 || next.Summary.Revision != next.Index.Revision || next.Raw.Revision != next.Index.Revision {
		return mem.Month{}, mem.ErrConflict
	}
	expected, err := mem.IndexFromSummary(next.Summary)
	if err != nil {
		return mem.Month{}, err
	}
	if next.Index.Content.Navigation != nil {
		if err := mem.ValidateNavigation(*next.Index.Content.Navigation, next.Summary); err != nil {
			return mem.Month{}, err
		}
		expected.Content.Navigation = next.Index.Content.Navigation
	}
	if !reflect.DeepEqual(next.Index, expected) {
		return mem.Month{}, errors.New("student memory: L1 must be derived exclusively from L2")
	}
	if len(next.Raw.Content.Turns) < len(old.Raw.Content.Turns) {
		return mem.Month{}, errors.New("student memory: raw history deletion denied")
	}
	for i, turn := range old.Raw.Content.Turns {
		if !reflect.DeepEqual(turn, next.Raw.Content.Turns[i]) {
			return mem.Month{}, errors.New("student memory: raw history rewrite denied")
		}
	}
	seen := map[string]bool{}
	for i, turn := range next.Raw.Content.Turns {
		key := mem.SourceID(turn.Source)
		if seen[key] {
			return mem.Month{}, errors.New("student memory: duplicate raw source")
		}
		seen[key] = true
		if turn.Seq != i+1 {
			return mem.Month{}, errors.New("student memory: raw seq not contiguous")
		}
	}
	i, err := mem.Encode(next.Index, MaxDocumentBytes)
	if err != nil {
		return mem.Month{}, err
	}
	s, err := mem.Encode(next.Summary, MaxDocumentBytes)
	if err != nil {
		return mem.Month{}, err
	}
	r, err := mem.Encode(next.Raw, MaxDocumentBytes)
	if err != nil {
		return mem.Month{}, err
	}
	next.Encoded = [3]string{string(i), string(s), string(r)}
	return next, nil
}
