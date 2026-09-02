package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Book is a catalogue entry plus its stock counters.
//
// TotalCopies is how many the library owns; AvailableCopies is how many are on
// the shelf right now. The difference is the number currently on loan, so the
// two counters together are the stock ledger — there is no separate "borrowed"
// count that could drift out of step with them.
type Book struct {
	ID              bson.ObjectID `bson:"_id,omitempty"     json:"id"`
	Title           string        `bson:"title"             json:"title"`
	Author          string        `bson:"author"            json:"author"`
	ISBN            string        `bson:"isbn"              json:"isbn"`
	Category        string        `bson:"category"          json:"category"`
	PublishedYear   int           `bson:"published_year"    json:"published_year"`
	TotalCopies     int           `bson:"total_copies"      json:"total_copies"`
	AvailableCopies int           `bson:"available_copies"  json:"available_copies"`
	CreatedAt       time.Time     `bson:"created_at"        json:"created_at"`
	UpdatedAt       time.Time     `bson:"updated_at"        json:"updated_at"`
}

// OnLoan reports how many copies are currently checked out.
func (b *Book) OnLoan() int { return b.TotalCopies - b.AvailableCopies }

// CreateBookRequest is the body of POST /books.
type CreateBookRequest struct {
	Title         string `json:"title"          binding:"required,min=1,max=300"`
	Author        string `json:"author"         binding:"required,min=1,max=200"`
	ISBN          string `json:"isbn"           binding:"required,isbn"`
	Category      string `json:"category"       binding:"required,min=1,max=100"`
	PublishedYear int    `json:"published_year" binding:"required,min=1450,max=2200"`
	TotalCopies   int    `json:"total_copies"   binding:"required,min=1,max=10000"`
}

// UpdateBookRequest is the body of PUT /books/:id. Every field is optional so a
// caller can send a partial update; nil means "leave unchanged".
//
// TotalCopies is applied as a delta to AvailableCopies rather than as an
// absolute overwrite — see BookRepository.Update — because copies that are
// currently on loan must stay accounted for.
type UpdateBookRequest struct {
	Title         *string `json:"title"          binding:"omitempty,min=1,max=300"`
	Author        *string `json:"author"         binding:"omitempty,min=1,max=200"`
	Category      *string `json:"category"       binding:"omitempty,min=1,max=100"`
	PublishedYear *int    `json:"published_year" binding:"omitempty,min=1450,max=2200"`
	TotalCopies   *int    `json:"total_copies"   binding:"omitempty,min=0,max=10000"`
}

// BookFilter narrows a catalogue listing. Zero values mean "no constraint".
type BookFilter struct {
	Search        string // matches title, author or ISBN
	Category      string
	AvailableOnly bool
	Page          int
	PerPage       int
}
