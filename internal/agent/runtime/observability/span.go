package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

type Record struct {
	TraceID, SpanID, ParentSpanID, Name, Status string
	StartedAt, EndedAt                          time.Time
	Attributes                                  map[string]string
}
type Exporter interface{ Export(context.Context, Record) }
type Tracer interface {
	Start(context.Context, string, map[string]string) (context.Context, Span)
}
type Span interface{ End(error) }
type identity struct{ trace, span string }
type traceKey struct{}
type tracer struct{ exporter Exporter }
type span struct {
	once     sync.Once
	ctx      context.Context
	record   Record
	exporter Exporter
}

func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func NewTracer(e Exporter) Tracer {
	if e == nil {
		e = LogExporter{}
	}
	return tracer{e}
}
func (t tracer) Start(ctx context.Context, name string, attrs map[string]string) (context.Context, Span) {
	p, _ := ctx.Value(traceKey{}).(identity)
	trace := p.trace
	if trace == "" {
		trace = randomID()
	}
	id := randomID()
	r := Record{TraceID: trace, SpanID: id, ParentSpanID: p.span, Name: name, StartedAt: time.Now().UTC(), Attributes: attrs}
	c := context.WithValue(ctx, traceKey{}, identity{trace, id})
	return c, &span{ctx: c, record: r, exporter: t.exporter}
}
func (s *span) End(err error) {
	s.once.Do(func() {
		s.record.EndedAt = time.Now().UTC()
		s.record.Status = "ok"
		if err != nil {
			s.record.Status = "error"
		}
		s.exporter.Export(context.WithoutCancel(s.ctx), s.record)
	})
}

type LogExporter struct{}

func (LogExporter) Export(ctx context.Context, r Record) {
	slog.DebugContext(ctx, "runtime.span", "trace_id", r.TraceID, "span_id", r.SpanID, "parent_span_id", r.ParentSpanID, "name", r.Name, "status", r.Status, "duration", r.EndedAt.Sub(r.StartedAt), "attributes", r.Attributes)
}

// Recorder is a bounded exporter for integration tests and local diagnostics.
type Recorder struct {
	mu      sync.Mutex
	records []Record
}

func (r *Recorder) Export(_ context.Context, v Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) >= 512 {
		r.records = r.records[1:]
	}
	r.records = append(r.records, v)
}
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.records...)
}
