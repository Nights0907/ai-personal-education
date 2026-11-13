package main

import (
	"context"
	"database/sql"
	"github.com/cloudwego/eino/components/model"
	agentdesign "github.com/musi-ai/education/agent-design"
	"github.com/musi-ai/education/internal/adapters/assets/local"
	"github.com/musi-ai/education/internal/adapters/model/bailian"
	"github.com/musi-ai/education/internal/adapters/ocr/mcpocr"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/photos"
	recallplanmysql "github.com/musi-ai/education/internal/adapters/persistence/mysql/recallplan"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teaching"

	"github.com/musi-ai/education/internal/agent/memory"
	runtime "github.com/musi-ai/education/internal/agent/runtime"

	"fmt"
	fsinmem "github.com/musi-ai/education/internal/agent/runtime/afs/inmem"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/model/proxy"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	studentmemory "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/guide"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/recallplan"
	"github.com/musi-ai/education/internal/modules/teaching/review"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/modules/teaching/teacher"
	"log"
	"os"
	"strconv"
	"time"
)

func buildTeacher(ctx context.Context, db *sql.DB, studentServices ...*studentmemory.Service) (*agentic.Pipeline, *solve.Service, *recallplan.Service, error) {
	repo := teaching.New(db)
	if e := repo.ValidateSchema(ctx); e != nil {
		return nil, nil, nil, e
	}
	service := &solve.Service{RadarDB: db, Repo: repo, Queries: store.NewQueries(store.New(db)), Store: store.New(db), ModelRef: os.Getenv("BAILIAN_CHAT_MODEL")}
	if os.Getenv("MUSI_OCR_MCP_URL") != "" {
		photoRepo := photos.New(db)
		if e := photoRepo.ValidateSchema(ctx); e != nil {
			return nil, nil, nil, e
		}
		transport := os.Getenv("MUSI_OCR_MCP_TRANSPORT")
		if transport == "" {
			transport = "streamableHttp"
		}
		ocr, e := mcpocr.New(mcpocr.Config{URL: os.Getenv("MUSI_OCR_MCP_URL"), Transport: transport, ToolName: os.Getenv("MUSI_OCR_TOOL_NAME")})
		if e != nil {
			return nil, nil, nil, e
		}
		root := os.Getenv("MUSI_ASSET_DIR")
		if root == "" {
			root = "var/private-assets"
		}
		files, e := local.New(root)
		if e != nil {
			return nil, nil, nil, e
		}
		go func() { <-ctx.Done(); files.Close() }()
		service.Photos = &photo.Service{Repo: photoRepo, Files: files, OCR: ocr}
		repo.Photos = service.Photos
	}
	if os.Getenv("BAILIAN_API_KEY") == "" || os.Getenv("BAILIAN_BASE_URL") == "" || os.Getenv("BAILIAN_CHAT_MODEL") == "" {
		return nil, service, nil, nil
	}
	limiter, e := bailian.NewLimiter(10)
	if e != nil {
		return nil, nil, nil, e
	}
	m, e := bailian.New(ctx, bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 4096, Timeout: 60 * time.Second}, limiter)
	if e != nil {
		return nil, nil, nil, e
	}
	solveModel, e := bailian.New(ctx, bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 8192, Timeout: 60 * time.Second}, limiter)
	if e != nil {
		return nil, nil, nil, e
	}
	m = &bailian.Dynamic{Role: "chat", Default: bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 4096, Timeout: 60 * time.Second}, Slots: limiter}
	solveModel = &bailian.Dynamic{Role: "chat", Default: bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 8192, Timeout: 60 * time.Second}, Slots: limiter}
	memoryModel := &bailian.Dynamic{Role: "memory", Default: bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 8192, Timeout: 60 * time.Second}, Slots: limiter}
	service.Reviewer = &bailian.Dynamic{Role: "radar", Default: bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 8192, Timeout: 60 * time.Second}, Slots: limiter}
	proxyService, e := proxy.NewService(map[string]model.BaseChatModel{"teacher": solveModel})
	if e != nil {
		return nil, nil, nil, e
	}
	contextBudget := 65536
	if raw := os.Getenv("TEACHER_CONTEXT_BUDGET"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 16384 || n > 262144 {
			return nil, nil, nil, fmt.Errorf("TEACHER_CONTEXT_BUDGET must be between 16384 and 262144")
		}
		contextBudget = n
	}
	spec := state.Spec{Name: "teacher", Version: "teacher-v2", InstructionFile: "agent-design/prompts/teacher-system.md", ModelRef: "teacher", TerminalTools: []string{"finalize_solution", "submit_solution"}, MaxIterations: 18, MaxToolCalls: 16, Timeout: 180 * time.Second, History: state.Policy{MaxInputTokens: contextBudget, OutputReserve: 8192, TriggerPercent: 80, KeepRecentTurns: 4, MaxSummaryTokens: 2048, Version: "teacher-v2"}}
	summaryModel := os.Getenv("BAILIAN_CHAT_MODEL")
	summaryClient, e := bailian.New(ctx, bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: summaryModel, MaxOutputTokens: 2048, Timeout: 60 * time.Second}, limiter)
	if e != nil {
		return nil, nil, nil, e
	}
	manager := &memory.Manager{ModelRef: summaryModel, Store: store.New(db), Counter: ctxbuild.ConservativeCounter{}, Summarizer: bailian.Summarizer{Model: summaryClient}}
	var onBudget runtime.ContextBudgetHandler = func(c context.Context, scope state.Scope, policy state.Policy) error {
		before, e := manager.Store.View(c, scope)
		if e != nil {
			return e
		}
		scheduled, e := manager.Schedule(c, scope, policy, true)
		if e != nil {
			return e
		}
		if !scheduled {
			return state.ErrBudget
		}
		wait, cancel := context.WithTimeout(c, 95*time.Second)
		defer cancel()
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-wait.Done():
				return wait.Err()
			case <-ticker.C:
				after, err := manager.Store.View(wait, scope)
				if err != nil {
					return err
				}
				if after.Summary.Revision > before.Summary.Revision {
					return nil
				}
			}
		}
	}

	chatModules := []agentic.Registration{}
	chatModules = append(chatModules, solve.Module{Service: service})
	reviewService := &review.Service{Repo: repo, Photos: service.Photos, Queries: service.Queries, Reviewer: m}
	chatModules = append(chatModules, review.Module{Service: reviewService})
	chatStore := store.New(db)
	guideService := &guide.Service{Repo: repo, Queries: store.NewQueries(chatStore), Reviewer: m}
	chatModules = append(chatModules, guide.Module{Service: guideService})
	var studentService *studentmemory.Service
	if len(studentServices) > 0 && studentServices[0] != nil {
		studentService = studentServices[0]
		studentService.Extractor = bailian.StudentMemoryExtractor{Model: memoryModel}
		studentService.Navigator = bailian.StudentMemoryExtractor{Model: memoryModel}
		module := studentmemory.Module{Service: studentService}
		chatModules = append(chatModules, module)
	}
	skillBody, e := agentdesign.Files.ReadFile("skills/math-solve/SKILL.md")
	if e != nil {
		return nil, nil, nil, e
	}
	skillFiles := map[string][]byte{"/skills/math-solve/SKILL.md": skillBody}
	if studentService != nil {
		body, err := agentdesign.Files.ReadFile("skills/student-memory/SKILL.md")
		if err != nil {
			return nil, nil, nil, err
		}
		skillFiles["/skills/student-memory/SKILL.md"] = body
	}
	reviewBody, err := agentdesign.Files.ReadFile("skills/review-recall/SKILL.md")
	if err != nil {
		return nil, nil, nil, err
	}
	skillFiles["/skills/review-recall/SKILL.md"] = reviewBody
	// Guided tutoring (引导学习) runs inside the chat pipeline: the guide module
	// authorizes this skill so the model can read the tutoring procedure.
	guideBody, err := agentdesign.Files.ReadFile("skills/step-tutor/SKILL.md")
	if err != nil {
		return nil, nil, nil, err
	}
	skillFiles["/skills/step-tutor/SKILL.md"] = guideBody
	for _, name := range []string{"question-variant", "question-verification"} {
		body, err := agentdesign.Files.ReadFile("skills/" + name + "/SKILL.md")
		if err != nil {
			return nil, nil, nil, err
		}
		skillFiles["/skills/"+name+"/SKILL.md"] = body
	}
	fs, e := fsinmem.New(skillFiles)
	if e != nil {
		return nil, nil, nil, e
	}
	pipeline, e := agentic.New(ctx, agentic.Dependencies{OnContextBudgetExceeded: onBudget, Store: chatStore, Models: proxy.Resolver(proxyService), Spec: spec, FileSystem: fs, Completion: teacher.Completion{Queries: service.Queries, Solutions: service, Store: chatStore}}, agentic.NewRegistry(chatModules...))
	if e != nil {
		return nil, nil, nil, e
	}
	go manager.Run(ctx, "server-summary", func(err error) { log.Printf("summary background task pending retry (%T)", err) })
	go service.RunRepair(ctx)
	if studentService != nil {
		go studentService.Run(ctx, func(err error) { log.Printf("student memory pending retry (%T)", err) })
	}
	// Forgetting-prevention scheduler: detects due knowledge points daily and
	// delivers a fixed number of questions in a fresh conversation.
	var rpService *recallplan.Service
	if studentService != nil {
		rpRepo := &recallplanmysql.Repository{DB: db}
		if err := rpRepo.Migrate(ctx); err != nil {
			return nil, nil, nil, err
		}
		rpService = &recallplan.Service{
			Repo:          rpRepo,
			Recall:        recallplan.MemoryRecall{Service: studentService},
			Questions:     recallplan.SolveQuestions{Repo: repo},
			Conversations: recallplan.AgentStoreSink{Queries: store.NewQueries(store.New(db))},
			Model:         summaryClient,
			SendHour:      recallplan.ParseSendHour(os.Getenv("MUSI_RECALL_SEND_HOUR")),
			Wake:          make(chan struct{}, 1),
		}
		// Auto-enroll + mark-reviewed: after each memory extraction, enroll the
		// student into a plan if anything is due, and mark answered review items
		// as reviewed.
		studentService.AfterEvidence = rpService.OnEvidence
		go rpService.Run(ctx, func(err error) { log.Printf("recall plan pending retry (%T)", err) })
	}
	return pipeline, service, rpService, nil
}
