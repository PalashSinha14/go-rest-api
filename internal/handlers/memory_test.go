package handlers

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/models"
	"go-server/internal/repository"
)

// The fakes below stand in for MongoDB so the HTTP layer can be tested without a
// database. Each guards its state with a mutex and performs its conditional
// updates while holding it, mirroring the per-document atomicity the real
// repositories get from MongoDB. That is what makes the concurrency tests
// meaningful rather than merely coincidental.

type fakeUserRepo struct {
	mu    sync.Mutex
	users map[bson.ObjectID]*models.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: map[bson.ObjectID]*models.User{}}
}

func (f *fakeUserRepo) Create(_ context.Context, u *models.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.users {
		if existing.Email == u.Email {
			return repository.ErrDuplicateEmail
		}
	}
	if u.ID.IsZero() {
		u.ID = bson.NewObjectID()
	}
	u.CreatedAt = time.Now().UTC()
	u.UpdatedAt = u.CreatedAt

	clone := *u
	f.users[u.ID] = &clone
	return nil
}

func (f *fakeUserRepo) FindByID(_ context.Context, id bson.ObjectID) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	clone := *u
	return &clone, nil
}

func (f *fakeUserRepo) FindByEmail(_ context.Context, email string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, u := range f.users {
		if u.Email == email {
			clone := *u
			return &clone, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) UpdateRole(_ context.Context, id bson.ObjectID, role models.Role) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	u.Role = role
	clone := *u
	return &clone, nil
}

func (f *fakeUserRepo) Count(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.users)), nil
}

type fakeBookRepo struct {
	mu    sync.Mutex
	books map[bson.ObjectID]*models.Book
	// failCreate is unused by the book endpoints but lets a loan test simulate a
	// mid-borrow failure.
	releaseErr error
}

func newFakeBookRepo() *fakeBookRepo {
	return &fakeBookRepo{books: map[bson.ObjectID]*models.Book{}}
}

// seed inserts a book with the given stock, returning its id.
func (f *fakeBookRepo) seed(title string, copies int) bson.ObjectID {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := bson.NewObjectID()
	f.books[id] = &models.Book{
		ID:              id,
		Title:           title,
		Author:          "Test Author",
		ISBN:            "9780306406157",
		Category:        "fiction",
		PublishedYear:   2000,
		TotalCopies:     copies,
		AvailableCopies: copies,
	}
	return id
}

func (f *fakeBookRepo) available(id bson.ObjectID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.books[id].AvailableCopies
}

func (f *fakeBookRepo) Create(_ context.Context, b *models.Book) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.books {
		if existing.ISBN == b.ISBN {
			return repository.ErrDuplicateISBN
		}
	}
	if b.ID.IsZero() {
		b.ID = bson.NewObjectID()
	}
	b.AvailableCopies = b.TotalCopies
	clone := *b
	f.books[b.ID] = &clone
	return nil
}

func (f *fakeBookRepo) FindByID(_ context.Context, id bson.ObjectID) (*models.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.books[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	clone := *b
	return &clone, nil
}

func (f *fakeBookRepo) List(_ context.Context, _ models.BookFilter) ([]*models.Book, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*models.Book, 0, len(f.books))
	for _, b := range f.books {
		clone := *b
		out = append(out, &clone)
	}
	return out, int64(len(out)), nil
}

func (f *fakeBookRepo) Update(_ context.Context, id bson.ObjectID, r models.UpdateBookRequest) (*models.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.books[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if r.Title != nil {
		b.Title = *r.Title
	}
	if r.TotalCopies != nil {
		delta := *r.TotalCopies - b.TotalCopies
		if b.AvailableCopies+delta < 0 {
			return nil, repository.ErrStockConflict
		}
		b.TotalCopies += delta
		b.AvailableCopies += delta
	}
	clone := *b
	return &clone, nil
}

func (f *fakeBookRepo) Delete(_ context.Context, id bson.ObjectID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.books[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.books, id)
	return nil
}

// ReserveCopy decrements under the lock, so the check and the write cannot be
// interleaved by another goroutine — the same guarantee the Mongo
// findOneAndUpdate gives us in production.
func (f *fakeBookRepo) ReserveCopy(_ context.Context, id bson.ObjectID) (*models.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.books[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if b.AvailableCopies <= 0 {
		return nil, repository.ErrNoCopies
	}
	b.AvailableCopies--
	clone := *b
	return &clone, nil
}

func (f *fakeBookRepo) ReleaseCopy(_ context.Context, id bson.ObjectID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.releaseErr != nil {
		return f.releaseErr
	}
	b, ok := f.books[id]
	if !ok {
		return repository.ErrNotFound
	}
	if b.AvailableCopies >= b.TotalCopies {
		return repository.ErrStockConflict
	}
	b.AvailableCopies++
	return nil
}

type fakeLoanRepo struct {
	mu        sync.Mutex
	loans     map[bson.ObjectID]*models.Loan
	createErr error // when set, Create fails, exercising the compensation path
}

func newFakeLoanRepo() *fakeLoanRepo {
	return &fakeLoanRepo{loans: map[bson.ObjectID]*models.Loan{}}
}

func (f *fakeLoanRepo) Create(_ context.Context, l *models.Loan) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.createErr != nil {
		return f.createErr
	}
	if l.ID.IsZero() {
		l.ID = bson.NewObjectID()
	}
	clone := *l
	f.loans[l.ID] = &clone
	return nil
}

func (f *fakeLoanRepo) FindByID(_ context.Context, id bson.ObjectID) (*models.Loan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	l, ok := f.loans[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	clone := *l
	return &clone, nil
}

func (f *fakeLoanRepo) List(_ context.Context, filter models.LoanFilter) ([]*models.Loan, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []*models.Loan{}
	for _, l := range f.loans {
		if filter.UserID != nil && l.UserID != *filter.UserID {
			continue
		}
		if filter.Status != "" && l.Status != filter.Status {
			continue
		}
		clone := *l
		out = append(out, &clone)
	}
	return out, int64(len(out)), nil
}

func (f *fakeLoanRepo) CountActiveByUser(_ context.Context, userID bson.ObjectID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var n int64
	for _, l := range f.loans {
		if l.UserID == userID && l.Status == models.LoanActive {
			n++
		}
	}
	return n, nil
}

func (f *fakeLoanRepo) HasActiveLoan(_ context.Context, userID, bookID bson.ObjectID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, l := range f.loans {
		if l.UserID == userID && l.BookID == bookID && l.Status == models.LoanActive {
			return true, nil
		}
	}
	return false, nil
}

// MarkReturned flips status under the lock and refuses a second close, matching
// the status-guarded update the Mongo implementation performs.
func (f *fakeLoanRepo) MarkReturned(_ context.Context, id bson.ObjectID, at time.Time) (*models.Loan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	l, ok := f.loans[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if l.Status != models.LoanActive {
		return nil, repository.ErrLoanClosed
	}
	l.Status = models.LoanReturned
	l.ReturnedAt = &at
	clone := *l
	return &clone, nil
}
