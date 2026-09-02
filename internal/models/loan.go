package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// LoanStatus is the stored lifecycle state of a loan. Note that "overdue" is not
// stored: it is derived from DueAt at read time, so a loan cannot sit in the
// database in a stale state just because no job has run to update it.
type LoanStatus string

const (
	// LoanActive means the book is still checked out.
	LoanActive LoanStatus = "active"
	// LoanReturned means the copy has been given back and stock restored.
	LoanReturned LoanStatus = "returned"
)

// Loan records one borrowing of one copy of a book.
type Loan struct {
	ID         bson.ObjectID `bson:"_id,omitempty"  json:"id"`
	BookID     bson.ObjectID `bson:"book_id"        json:"book_id"`
	UserID     bson.ObjectID `bson:"user_id"        json:"user_id"`
	Status     LoanStatus    `bson:"status"         json:"status"`
	BorrowedAt time.Time     `bson:"borrowed_at"    json:"borrowed_at"`
	DueAt      time.Time     `bson:"due_at"         json:"due_at"`
	ReturnedAt *time.Time    `bson:"returned_at"    json:"returned_at,omitempty"`

	// Book is populated by repository lookups that join the catalogue, so a
	// client listing loans does not have to fetch each title separately.
	Book *Book `bson:"book,omitempty" json:"book,omitempty"`
}

// IsOverdue reports whether an active loan has passed its due date as of now.
// Returned loans are never overdue, however late they were given back.
func (l *Loan) IsOverdue(now time.Time) bool {
	return l.Status == LoanActive && now.After(l.DueAt)
}

// DaysOverdue returns how many whole days past due an active loan is, or 0.
func (l *Loan) DaysOverdue(now time.Time) int {
	if !l.IsOverdue(now) {
		return 0
	}
	return int(now.Sub(l.DueAt).Hours() / 24)
}

// BorrowRequest is the body of POST /loans/borrow.
type BorrowRequest struct {
	BookID string `json:"book_id" binding:"required,mongoid"`
}

// LoanFilter narrows a loan listing. Zero values mean "no constraint".
type LoanFilter struct {
	UserID  *bson.ObjectID
	BookID  *bson.ObjectID
	Status  LoanStatus
	Overdue bool
	Page    int
	PerPage int
}

// LoanView is a loan enriched with the derived fields a client would otherwise
// have to compute itself.
type LoanView struct {
	*Loan
	Overdue     bool `json:"overdue"`
	DaysOverdue int  `json:"days_overdue"`
}

// NewLoanView wraps a loan with its derived overdue state as of now.
func NewLoanView(l *Loan, now time.Time) LoanView {
	return LoanView{Loan: l, Overdue: l.IsOverdue(now), DaysOverdue: l.DaysOverdue(now)}
}
