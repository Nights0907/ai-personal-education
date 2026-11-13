package studentmemory

import (
	"context"
	"encoding/json"
	"errors"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"sort"
)

// MongoDocuments keeps Redis as the read cache and MongoDB as the durable authority.
// One Mongo value contains all three layers so a revision is committed atomically.
type MongoDocuments struct {
	Cache      *Documents
	Collection *mongo.Collection
}
type mongoMonth struct {
	Key      string `bson:"_id"`
	Student  string `bson:"student_id"`
	Month    string `bson:"month"`
	Revision int64  `bson:"revision"`
	Value    string `bson:"value"`
}

func (d *MongoDocuments) Migrate(ctx context.Context) error {
	_, e := d.Collection.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "student_id", Value: 1}, {Key: "month", Value: -1}}})
	return e
}
func memoryKey(student, month string) (string, error) {
	p, e := mem.MonthlyPaths(student, month)
	if e != nil {
		return "", e
	}
	return p.Summary, e
}
func (d *MongoDocuments) durable(ctx context.Context, student, month string) (mem.Month, error) {
	key, e := memoryKey(student, month)
	if e != nil {
		return mem.Month{}, e
	}
	var row mongoMonth
	e = d.Collection.FindOne(ctx, bson.M{"_id": key, "student_id": student}).Decode(&row)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return mem.Month{}, mem.ErrNotFound
	}
	if e != nil {
		return mem.Month{}, e
	}
	var m mem.Month
	if e = json.Unmarshal([]byte(row.Value), &m); e != nil {
		return m, e
	}
	if m.Index.StudentID != student || m.Summary.StudentID != student || m.Raw.StudentID != student || m.Raw.Month != month || m.Index.Revision != row.Revision || m.Summary.Revision != row.Revision || m.Raw.Revision != row.Revision {
		return mem.Month{}, errors.New("Mongo memory identity or revision mismatch")
	}
	return m, nil
}

var fillMongoCache = redis.NewScript(`local r=tonumber(redis.call('GET',KEYS[4]) or '0');if r>tonumber(ARGV[4]) then return 0 end;for i=1,3 do redis.call('SET',KEYS[i],ARGV[i]) end;redis.call('SET',KEYS[4],ARGV[4]);return 1`)

func (d *MongoDocuments) cache(ctx context.Context, student, month string, m mem.Month) {
	if d.Cache == nil {
		return
	}
	p, e := mem.MonthlyPaths(student, month)
	if e != nil {
		return
	}
	_, _ = fillMongoCache.Run(ctx, d.Cache.Client, []string{p.Index, p.Summary, p.Content, p.Summary + ":revision"}, m.Encoded[0], m.Encoded[1], m.Encoded[2], m.Raw.Revision).Result()
}
func (d *MongoDocuments) Load(ctx context.Context, student, month string) (mem.Month, error) {
	if _, e := memoryKey(student, month); e != nil {
		return mem.Month{}, e
	}
	if d.Cache != nil {
		if m, e := d.Cache.Load(ctx, student, month); e == nil {
			return m, nil
		}
	}
	m, e := d.durable(ctx, student, month)
	if e == nil {
		d.cache(ctx, student, month, m)
	}
	return m, e
}
func (d *MongoDocuments) Publish(ctx context.Context, student, month string, old, next mem.Month) error {
	saved, e := validatedMonth(student, month, old, next)
	if e != nil {
		return e
	}
	key, e := memoryKey(student, month)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(saved)
	if e != nil {
		return e
	}
	row := mongoMonth{key, student, month, saved.Raw.Revision, string(raw)}
	if old.Raw.Revision == 0 {
		_, e = d.Collection.InsertOne(ctx, row)
		if mongo.IsDuplicateKeyError(e) {
			return mem.ErrConflict
		}
		if e != nil {
			return e
		}
	} else {
		res, e := d.Collection.ReplaceOne(ctx, bson.M{"_id": key, "student_id": student, "revision": old.Raw.Revision}, row)
		if e != nil {
			return e
		}
		if res.MatchedCount != 1 {
			return mem.ErrConflict
		}
	}
	d.cache(ctx, student, month, saved)
	return nil
}
func (d *MongoDocuments) Indexes(ctx context.Context, student string, limit int) ([]mem.Document[mem.IndexContent], error) {
	if limit < 1 || limit > 120 {
		return nil, mem.ErrConflict
	}
	if _, e := memoryKey(student, "2026-01"); e != nil {
		return nil, e
	}
	cursor, e := d.Collection.Find(ctx, bson.M{"student_id": student}, options.Find().SetSort(bson.D{{Key: "month", Value: -1}}).SetLimit(int64(limit)).SetProjection(bson.M{"month": 1}))
	if e != nil {
		return nil, e
	}
	defer cursor.Close(ctx)
	var months []string
	for cursor.Next(ctx) {
		var row mongoMonth
		if e = cursor.Decode(&row); e != nil {
			return nil, e
		}
		months = append(months, row.Month)
	}
	if e = cursor.Err(); e != nil {
		return nil, e
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	out := []mem.Document[mem.IndexContent]{}
	for _, month := range months {
		m, e := d.Load(ctx, student, month)
		if e != nil {
			return nil, e
		}
		out = append(out, m.Index)
	}
	return out, nil
}
