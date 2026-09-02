package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"go-server/internal/db"
	"go-server/internal/models"
)

type userRepository struct {
	col *mongo.Collection
}

// NewUserRepository returns a MongoDB-backed UserRepository.
func NewUserRepository(database *mongo.Database) UserRepository {
	return &userRepository{col: database.Collection(db.CollectionUsers)}
}

// normaliseEmail lower-cases and trims an address so that lookup and the unique
// index agree on what counts as the same account.
func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (r *userRepository) Create(ctx context.Context, u *models.User) error {
	now := time.Now().UTC()
	u.Email = normaliseEmail(u.Email)
	u.CreatedAt = now
	u.UpdatedAt = now
	if u.ID.IsZero() {
		u.ID = bson.NewObjectID()
	}

	if _, err := r.col.InsertOne(ctx, u); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrDuplicateEmail
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *userRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.User, error) {
	return r.findOne(ctx, bson.M{"_id": id})
}

func (r *userRepository) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	return r.findOne(ctx, bson.M{"email": normaliseEmail(email)})
}

func (r *userRepository) findOne(ctx context.Context, filter bson.M) (*models.User, error) {
	var u models.User
	err := r.col.FindOne(ctx, filter).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	return &u, nil
}

func (r *userRepository) UpdateRole(ctx context.Context, id bson.ObjectID, role models.Role) (*models.User, error) {
	update := bson.M{"$set": bson.M{"role": role, "updated_at": time.Now().UTC()}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var u models.User
	err := r.col.FindOneAndUpdate(ctx, bson.M{"_id": id}, update, opts).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update user role: %w", err)
	}
	return &u, nil
}

func (r *userRepository) Count(ctx context.Context) (int64, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}
