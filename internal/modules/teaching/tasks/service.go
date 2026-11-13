package tasks

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/query"
	"log"
	"time"
)

var ErrNotFound = errors.New("teaching task: not found")
var ErrConflict = errors.New("teaching task: conflict")

type Task struct {
	ID             string            `json:"id"`
	StudentID      string            `json:"-"`
	ConversationID string            `json:"conversation_id"`
	Input          query.Input       `json:"input"`
	ParentID       string            `json:"parent_task_id,omitempty"`
	Status         string            `json:"status"`
	Opening        string            `json:"opening,omitempty"`
	Result         map[string]string `json:"result,omitempty"`
	Error          string            `json:"error,omitempty"`
	Token          string            `json:"-"`
	Attempts       int               `json:"attempts"`
	CreatedAt      time.Time         `json:"created_at"`
}
type Event struct {
	ID   int64             `json:"id"`
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}
type Repository interface {
	Create(context.Context, string, query.Input, string) (Task, error)
	Get(context.Context, string, string) (Task, error)
	List(context.Context, string, string) ([]Task, error)
	Events(context.Context, string, string, int64) ([]Event, error)
	Claim(context.Context, time.Time) (Task, error)
	Emit(context.Context, Task, string, map[string]string) (string, error)
	Finish(context.Context, Task, map[string]string, error) error
	Retry(context.Context, string, string) error
}
type Execute func(context.Context, Task, func(string, map[string]string) (string, error)) (map[string]string, error)
type Service struct {
	Repo    Repository
	Execute Execute
	Wake    chan struct{}
}

func (s *Service) Notify() {
	select {
	case s.Wake <- struct{}{}:
	default:
	}
}
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, e := s.Step(ctx)
		if e != nil && ctx.Err() == nil {
			log.Printf("teaching task pending: %v", e)
		}
		delay := time.Second
		if !worked {
			delay = 3 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.Wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (s *Service) Step(ctx context.Context) (bool, error) {
	t, e := s.Repo.Claim(ctx, time.Now().UTC())
	if errors.Is(e, ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	work, cancel := context.WithTimeout(ctx, 185*time.Second)
	result, runErr := s.Execute(work, t, func(kind string, data map[string]string) (string, error) { return s.Repo.Emit(work, t, kind, data) })
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	} // retain lease for crash-safe recovery
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	return true, s.Repo.Finish(cleanup, t, result, runErr)
}
