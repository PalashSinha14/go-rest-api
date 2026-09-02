// Package repository defines the storage contracts the HTTP layer depends on and
// provides their MongoDB implementations.
//
// Handlers take these interfaces rather than concrete Mongo types, which keeps
// the transport layer testable against in-memory fakes and confines every
// driver-specific detail to this package.
package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/models"
)

// Sentinel errors the HTTP layer maps onto status codes. Returning these rather
// than raw driver errors is what lets handlers stay free of Mongo imports.
var (
	ErrNotFound       = errors.New("resource not found")
	ErrDuplicateEmail = errors.New("email is already registered")
	ErrDuplicateISBN  = errors.New("isbn is already in the catalogue")
	ErrNoCopies       = errors.New("no copies available to borrow")
	ErrAlreadyLoaned  = errors.New("this book is already on loan to you")
	ErrLoanClosed     = errors.New("loan has already been returned")
	ErrStockConflict  = errors.New("stock would be inconsistent with active loans")
)

// UserRepository stores library accounts.
type UserRepository interface {
	Create(ctx context.Context, u *models.User) error
	FindByID(ctx context.Context, id bson.ObjectID) (*models.User, error)
	FindByEmail(ctx context.Context, email string) (*models.User, error)
	UpdateRole(ctx context.Context, id bson.ObjectID, role models.Role) (*models.User, error)
	Count(ctx context.Context) (int64, error)
}

// BookRepository stores the catalogue and its stock counters.
type BookRepository interface {
	Create(ctx context.Context, b *models.Book) error
	FindByID(ctx context.Context, id bson.ObjectID) (*models.Book, error)
	List(ctx context.Context, f models.BookFilter) ([]*models.Book, int64, error)
	Update(ctx context.Context, id bson.ObjectID, r models.UpdateBookRequest) (*models.Book, error)
	Delete(ctx context.Context, id bson.ObjectID) error

	// ReserveCopy atomically decrements AvailableCopies, returning ErrNoCopies if
	// none are free. It must be a single conditional update rather than a
	// read-then-write, or two concurrent borrowers can both observe the last copy.
	ReserveCopy(ctx context.Context, id bson.ObjectID) (*models.Book, error)

	// ReleaseCopy atomically increments AvailableCopies when a book comes back.
	ReleaseCopy(ctx context.Context, id bson.ObjectID) error
}

// LoanRepository stores borrow/return records.
type LoanRepository interface {
	Create(ctx context.Context, l *models.Loan) error
	FindByID(ctx context.Context, id bson.ObjectID) (*models.Loan, error)
	List(ctx context.Context, f models.LoanFilter) ([]*models.Loan, int64, error)
	CountActiveByUser(ctx context.Context, userID bson.ObjectID) (int64, error)
	HasActiveLoan(ctx context.Context, userID, bookID bson.ObjectID) (bool, error)

	// MarkReturned closes an active loan. It returns ErrLoanClosed if the loan was
	// already returned, so a double-return cannot inflate stock.
	MarkReturned(ctx context.Context, id bson.ObjectID, returnedAt time.Time) (*models.Loan, error)
}
