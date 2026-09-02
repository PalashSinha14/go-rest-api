package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"go-server/internal/db"
	"go-server/internal/models"
)

type bookRepository struct {
	col *mongo.Collection
}

// NewBookRepository returns a MongoDB-backed BookRepository.
func NewBookRepository(database *mongo.Database) BookRepository {
	return &bookRepository{col: database.Collection(db.CollectionBooks)}
}

func (r *bookRepository) Create(ctx context.Context, b *models.Book) error {
	now := time.Now().UTC()
	b.CreatedAt = now
	b.UpdatedAt = now
	b.AvailableCopies = b.TotalCopies
	if b.ID.IsZero() {
		b.ID = bson.NewObjectID()
	}

	if _, err := r.col.InsertOne(ctx, b); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrDuplicateISBN
		}
		return fmt.Errorf("insert book: %w", err)
	}
	return nil
}

func (r *bookRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Book, error) {
	var b models.Book
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find book: %w", err)
	}
	return &b, nil
}

func (r *bookRepository) List(ctx context.Context, f models.BookFilter) ([]*models.Book, int64, error) {
	filter := bson.M{}

	// Substring matching rather than a $text index: readers search for fragments
	// ("harr", an ISBN prefix), and $text only matches whole stemmed words.
	if f.Search != "" {
		pattern := regexp.QuoteMeta(f.Search)
		rx := bson.M{"$regex": pattern, "$options": "i"}
		filter["$or"] = []bson.M{
			{"title": rx},
			{"author": rx},
			{"isbn": rx},
		}
	}
	if f.Category != "" {
		filter["category"] = f.Category
	}
	if f.AvailableOnly {
		filter["available_copies"] = bson.M{"$gt": 0}
	}

	total, err := r.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count books: %w", err)
	}

	page, perPage := normalisePaging(f.Page, f.PerPage)
	opts := options.Find().
		SetSort(bson.D{{Key: "title", Value: 1}}).
		SetSkip(int64((page - 1) * perPage)).
		SetLimit(int64(perPage))

	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("list books: %w", err)
	}
	defer cur.Close(ctx)

	books := []*models.Book{}
	if err := cur.All(ctx, &books); err != nil {
		return nil, 0, fmt.Errorf("decode books: %w", err)
	}
	return books, total, nil
}

// Update applies a partial change.
//
// Changing TotalCopies is the delicate part: AvailableCopies must move by the
// same delta so that copies currently on loan stay accounted for. Setting
// available directly would either lose or invent stock. The update is guarded by
// the total_copies value we read, so a concurrent update fails rather than
// applying its delta to a figure that has since moved.
func (r *bookRepository) Update(ctx context.Context, id bson.ObjectID, req models.UpdateBookRequest) (*models.Book, error) {
	current, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	set := bson.M{"updated_at": time.Now().UTC()}
	if req.Title != nil {
		set["title"] = *req.Title
	}
	if req.Author != nil {
		set["author"] = *req.Author
	}
	if req.Category != nil {
		set["category"] = *req.Category
	}
	if req.PublishedYear != nil {
		set["published_year"] = *req.PublishedYear
	}

	update := bson.M{"$set": set}
	filter := bson.M{"_id": id}

	if req.TotalCopies != nil {
		delta := *req.TotalCopies - current.TotalCopies
		// Shrinking below what readers are holding would make available go
		// negative, so refuse rather than corrupt the ledger.
		if current.AvailableCopies+delta < 0 {
			return nil, fmt.Errorf("%w: %d copies are on loan", ErrStockConflict, current.OnLoan())
		}
		if delta != 0 {
			update["$inc"] = bson.M{"total_copies": delta, "available_copies": delta}
			filter["total_copies"] = current.TotalCopies
		}
	}

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var updated models.Book
	err = r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// Either the book vanished or its stock moved under us.
		if _, findErr := r.FindByID(ctx, id); findErr != nil {
			return nil, findErr
		}
		return nil, ErrStockConflict
	}
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrDuplicateISBN
		}
		return nil, fmt.Errorf("update book: %w", err)
	}
	return &updated, nil
}

func (r *bookRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("delete book: %w", err)
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ReserveCopy takes one copy off the shelf.
//
// The whole borrow path hinges on this being a single conditional update. A
// read-then-write would let two concurrent borrowers both see availableCopies==1
// and both succeed, driving the count to -1 and lending a book that isn't there.
// Expressing the precondition as part of the filter makes MongoDB's per-document
// atomicity do the work, with no transaction or application-level lock needed.
func (r *bookRepository) ReserveCopy(ctx context.Context, id bson.ObjectID) (*models.Book, error) {
	filter := bson.M{"_id": id, "available_copies": bson.M{"$gt": 0}}
	update := bson.M{
		"$inc": bson.M{"available_copies": -1},
		"$set": bson.M{"updated_at": time.Now().UTC()},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var b models.Book
	err := r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// The filter matched nothing: either no such book, or it is all lent out.
		if _, findErr := r.FindByID(ctx, id); findErr != nil {
			return nil, findErr
		}
		return nil, ErrNoCopies
	}
	if err != nil {
		return nil, fmt.Errorf("reserve copy: %w", err)
	}
	return &b, nil
}

// ReleaseCopy puts a copy back, capped at total_copies so a replayed return
// cannot inflate stock beyond what the library owns.
func (r *bookRepository) ReleaseCopy(ctx context.Context, id bson.ObjectID) error {
	filter := bson.M{"_id": id, "$expr": bson.M{"$lt": []any{"$available_copies", "$total_copies"}}}
	update := bson.M{
		"$inc": bson.M{"available_copies": 1},
		"$set": bson.M{"updated_at": time.Now().UTC()},
	}

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("release copy: %w", err)
	}
	if res.MatchedCount == 0 {
		if _, findErr := r.FindByID(ctx, id); findErr != nil {
			return findErr
		}
		return ErrStockConflict
	}
	return nil
}

// normalisePaging clamps user-supplied paging into a sane range.
func normalisePaging(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case perPage < 1:
		perPage = 20
	case perPage > 100:
		perPage = 100
	}
	return page, perPage
}
