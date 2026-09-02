// Package db owns the MongoDB connection and the index definitions the
// repositories rely on for correctness and speed.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection names, referenced by the repositories.
const (
	CollectionUsers = "users"
	CollectionBooks = "books"
	CollectionLoans = "loans"
)

// Mongo wraps a live client and the handle to our database.
type Mongo struct {
	Client   *mongo.Client
	Database *mongo.Database
}

// Connect dials MongoDB and verifies the connection with a ping, so a bad URI
// fails at startup rather than on the first request.
func Connect(ctx context.Context, uri, database string) (*Mongo, error) {
	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(10 * time.Second).
		SetConnectTimeout(10 * time.Second)

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	return &Mongo{Client: client, Database: client.Database(database)}, nil
}

// Disconnect closes the connection pool.
func (m *Mongo) Disconnect(ctx context.Context) error {
	return m.Client.Disconnect(ctx)
}

// EnsureIndexes creates every index the application depends on. It is safe to
// call on every boot: MongoDB treats creating an existing index as a no-op.
//
// Two of these are load-bearing rather than merely fast. The unique index on
// users.email is what makes concurrent registration of the same address fail for
// one of the two callers instead of silently creating a duplicate account, and
// the unique index on books.isbn does the same for the catalogue.
func EnsureIndexes(ctx context.Context, database *mongo.Database, log *slog.Logger) error {
	specs := []struct {
		collection string
		models     []mongo.IndexModel
	}{
		{
			collection: CollectionUsers,
			models: []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "email", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("uniq_email"),
				},
			},
		},
		{
			collection: CollectionBooks,
			models: []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "isbn", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("uniq_isbn"),
				},
				{
					Keys:    bson.D{{Key: "category", Value: 1}},
					Options: options.Index().SetName("by_category"),
				},
				// Serves the default title-ordered catalogue listing. Note that the
				// search itself is an unanchored regex, which cannot use an index;
				// at catalogue scale that is fine, and it buys substring matching
				// that a $text index cannot do. Swapping to Atlas Search would be
				// the move if the collection ever outgrew a collection scan.
				{
					Keys:    bson.D{{Key: "title", Value: 1}},
					Options: options.Index().SetName("by_title"),
				},
			},
		},
		{
			collection: CollectionLoans,
			models: []mongo.IndexModel{
				// Serves "my loans" and the active-loan-count quota check.
				{
					Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}},
					Options: options.Index().SetName("by_user_status"),
				},
				{
					Keys:    bson.D{{Key: "book_id", Value: 1}, {Key: "status", Value: 1}},
					Options: options.Index().SetName("by_book_status"),
				},
				// Serves the overdue listing.
				{
					Keys:    bson.D{{Key: "status", Value: 1}, {Key: "due_at", Value: 1}},
					Options: options.Index().SetName("by_status_due"),
				},
			},
		},
	}

	for _, spec := range specs {
		created, err := database.Collection(spec.collection).Indexes().
			CreateMany(ctx, spec.models)
		if err != nil {
			return fmt.Errorf("create indexes on %s: %w", spec.collection, err)
		}
		log.Debug("indexes ensured",
			slog.String("collection", spec.collection),
			slog.Any("indexes", created),
		)
	}

	log.Info("mongo indexes ensured", slog.Int("collections", len(specs)))
	return nil
}
