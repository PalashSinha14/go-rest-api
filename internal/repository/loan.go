package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"go-server/internal/db"
	"go-server/internal/models"
)

type loanRepository struct {
	col *mongo.Collection
}

// NewLoanRepository returns a MongoDB-backed LoanRepository.
func NewLoanRepository(database *mongo.Database) LoanRepository {
	return &loanRepository{col: database.Collection(db.CollectionLoans)}
}

func (r *loanRepository) Create(ctx context.Context, l *models.Loan) error {
	if l.ID.IsZero() {
		l.ID = bson.NewObjectID()
	}
	if _, err := r.col.InsertOne(ctx, l); err != nil {
		return fmt.Errorf("insert loan: %w", err)
	}
	return nil
}

func (r *loanRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Loan, error) {
	var l models.Loan
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find loan: %w", err)
	}
	return &l, nil
}

// List returns loans matching f, each joined to its book so a client rendering a
// borrowing history does not have to issue one catalogue request per row.
func (r *loanRepository) List(ctx context.Context, f models.LoanFilter) ([]*models.Loan, int64, error) {
	match := bson.M{}
	if f.UserID != nil {
		match["user_id"] = *f.UserID
	}
	if f.BookID != nil {
		match["book_id"] = *f.BookID
	}
	if f.Status != "" {
		match["status"] = f.Status
	}
	if f.Overdue {
		match["status"] = models.LoanActive
		match["due_at"] = bson.M{"$lt": time.Now().UTC()}
	}

	total, err := r.col.CountDocuments(ctx, match)
	if err != nil {
		return nil, 0, fmt.Errorf("count loans: %w", err)
	}

	page, perPage := normalisePaging(f.Page, f.PerPage)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$sort", Value: bson.D{{Key: "borrowed_at", Value: -1}}}},
		{{Key: "$skip", Value: int64((page - 1) * perPage)}},
		{{Key: "$limit", Value: int64(perPage)}},
		{{Key: "$lookup", Value: bson.M{
			"from":         db.CollectionBooks,
			"localField":   "book_id",
			"foreignField": "_id",
			"as":           "book",
		}}},
		// The join yields an array; flatten it, keeping loans whose book has since
		// been removed from the catalogue rather than dropping them from history.
		{{Key: "$unwind", Value: bson.M{
			"path":                       "$book",
			"preserveNullAndEmptyArrays": true,
		}}},
	}

	cur, err := r.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, 0, fmt.Errorf("list loans: %w", err)
	}
	defer cur.Close(ctx)

	loans := []*models.Loan{}
	if err := cur.All(ctx, &loans); err != nil {
		return nil, 0, fmt.Errorf("decode loans: %w", err)
	}
	return loans, total, nil
}

func (r *loanRepository) CountActiveByUser(ctx context.Context, userID bson.ObjectID) (int64, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{"user_id": userID, "status": models.LoanActive})
	if err != nil {
		return 0, fmt.Errorf("count active loans: %w", err)
	}
	return n, nil
}

func (r *loanRepository) HasActiveLoan(ctx context.Context, userID, bookID bson.ObjectID) (bool, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{
		"user_id": userID,
		"book_id": bookID,
		"status":  models.LoanActive,
	}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("check active loan: %w", err)
	}
	return n > 0, nil
}

// MarkReturned closes an active loan.
//
// Matching on status=="active" as part of the filter is what makes a double
// return safe: the second call matches nothing and reports ErrLoanClosed, so the
// caller never goes on to increment stock a second time.
func (r *loanRepository) MarkReturned(ctx context.Context, id bson.ObjectID, returnedAt time.Time) (*models.Loan, error) {
	filter := bson.M{"_id": id, "status": models.LoanActive}
	update := bson.M{"$set": bson.M{
		"status":      models.LoanReturned,
		"returned_at": returnedAt,
	}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var l models.Loan
	err := r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		if _, findErr := r.FindByID(ctx, id); findErr != nil {
			return nil, findErr
		}
		return nil, ErrLoanClosed
	}
	if err != nil {
		return nil, fmt.Errorf("mark loan returned: %w", err)
	}
	return &l, nil
}
