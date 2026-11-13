package main

import (
	"context"
	"database/sql"
	"errors"
	"go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
	"os"
	"time"

	sqlmemory "github.com/musi-ai/education/internal/adapters/persistence/mysql/studentmemory"
	redismemory "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/redis/go-redis/v9"
)

func buildStudentMemory(ctx context.Context, db *sql.DB) (*mem.Service, func(), error) {
	if os.Getenv("MUSI_REDIS_URL") == "" {
		return nil, func() {}, nil
	}
	options, err := redis.ParseURL(os.Getenv("MUSI_REDIS_URL"))
	if err != nil {
		return nil, func() {}, errors.New("invalid MUSI_REDIS_URL")
	}
	options.Protocol = 2
	options.DisableIdentity = true
	options.DialTimeout = 3 * time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = 3 * time.Second
	client := redis.NewClient(options)
	close := func() { _ = client.Close() }
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = client.Ping(probe).Err(); err != nil {
		close()
		return nil, func() {}, errors.New("student memory Redis unavailable")
	}
	repo := &sqlmemory.Repository{DB: db}
	if err = repo.EnsureEvidenceSchema(probe); err != nil {
		close()
		return nil, func() {}, err
	}
	if err = repo.ValidateSchema(probe); err != nil {
		close()
		return nil, func() {}, errors.New("student memory schema missing; run cmd/runtime-db")
	}
	uri := os.Getenv("MUSI_MONGO_URI")
	if uri == "" {
		uri = "mongodb://127.0.0.1:27017"
	}
	mc, e := mongo.Connect(ctx, mongooptions.Client().ApplyURI(uri).SetServerSelectionTimeout(5*time.Second))
	if e != nil {
		close()
		return nil, func() {}, e
	}
	if e = mc.Ping(probe, nil); e != nil {
		close()
		_ = mc.Disconnect(context.Background())
		return nil, func() {}, errors.New("student memory MongoDB unavailable")
	}
	redisClose := close
	close = func() { redisClose(); _ = mc.Disconnect(context.Background()) }
	database := os.Getenv("MUSI_MONGO_DATABASE")
	if database == "" {
		database = "musi_education"
	}
	docs := &redismemory.MongoDocuments{Collection: mc.Database(database).Collection("student_memory"), Cache: &redismemory.Documents{Client: client}}
	if err = docs.Migrate(ctx); err != nil {
		close()
		return nil, func() {}, err
	}
	return &mem.Service{Repo: repo, Documents: docs, Wake: make(chan struct{}, 1)}, close, nil
}
