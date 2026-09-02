package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/httpx"
	"go-server/internal/middleware"
	"go-server/internal/models"
	"go-server/internal/repository"
)

// BookHandler serves the catalogue.
type BookHandler struct {
	books repository.BookRepository
}

// NewBookHandler wires a BookHandler to its repository.
func NewBookHandler(books repository.BookRepository) *BookHandler {
	return &BookHandler{books: books}
}

// Create adds a book to the catalogue. Librarian-only.
func (h *BookHandler) Create(c *gin.Context) {
	req := middleware.Payload[models.CreateBookRequest](c)

	book := &models.Book{
		Title:         req.Title,
		Author:        req.Author,
		ISBN:          req.ISBN,
		Category:      req.Category,
		PublishedYear: req.PublishedYear,
		TotalCopies:   req.TotalCopies,
	}

	if err := h.books.Create(c.Request.Context(), book); err != nil {
		respondRepoError(c, err, "")
		return
	}

	middleware.LoggerOf(c).Info("book created",
		slog.String("book_id", book.ID.Hex()),
		slog.String("isbn", book.ISBN),
		slog.Int("copies", book.TotalCopies),
	)
	httpx.Success(c, http.StatusCreated, book)
}

// List returns a paginated, filterable view of the catalogue.
func (h *BookHandler) List(c *gin.Context) {
	filter := models.BookFilter{
		Search:        c.Query("search"),
		Category:      c.Query("category"),
		AvailableOnly: c.Query("available") == "true",
		Page:          queryInt(c, "page", 1),
		PerPage:       queryInt(c, "per_page", 20),
	}

	books, total, err := h.books.List(c.Request.Context(), filter)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}

	page, perPage := pagingOf(filter.Page, filter.PerPage)
	httpx.SuccessWithMeta(c, http.StatusOK, books, httpx.Meta{
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages(total, perPage),
	})
}

// Get returns one book by id.
func (h *BookHandler) Get(c *gin.Context) {
	id, ok := objectIDParam(c, "id", "book id")
	if !ok {
		return
	}

	book, err := h.books.FindByID(c.Request.Context(), id)
	if err != nil {
		respondRepoError(c, err, "book not found")
		return
	}
	httpx.Success(c, http.StatusOK, book)
}

// Update applies a partial change to a book. Librarian-only.
func (h *BookHandler) Update(c *gin.Context) {
	id, ok := objectIDParam(c, "id", "book id")
	if !ok {
		return
	}

	req := middleware.Payload[models.UpdateBookRequest](c)

	book, err := h.books.Update(c.Request.Context(), id, req)
	if err != nil {
		respondRepoError(c, err, "book not found")
		return
	}

	middleware.LoggerOf(c).Info("book updated", slog.String("book_id", book.ID.Hex()))
	httpx.Success(c, http.StatusOK, book)
}

// Delete removes a book from the catalogue. Librarian-only.
//
// Copies that are out on loan block the delete: removing the catalogue entry
// would orphan those loans and lose the record that someone still holds the book.
func (h *BookHandler) Delete(c *gin.Context) {
	id, ok := objectIDParam(c, "id", "book id")
	if !ok {
		return
	}

	book, err := h.books.FindByID(c.Request.Context(), id)
	if err != nil {
		respondRepoError(c, err, "book not found")
		return
	}
	if book.OnLoan() > 0 {
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"cannot delete a book while copies are on loan")
		return
	}

	if err := h.books.Delete(c.Request.Context(), id); err != nil {
		respondRepoError(c, err, "book not found")
		return
	}

	middleware.LoggerOf(c).Info("book deleted", slog.String("book_id", id.Hex()))
	c.Status(http.StatusNoContent)
}

// objectIDParam parses a path parameter as an ObjectID, writing a 400 and
// reporting false if it is malformed.
func objectIDParam(c *gin.Context, param, label string) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(c.Param(param))
	if err != nil {
		httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
			label+" is not a valid id")
		return bson.NilObjectID, false
	}
	return id, true
}

// queryInt reads an integer query parameter, falling back when absent or invalid.
func queryInt(c *gin.Context, key string, fallback int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return fallback
}

// pagingOf mirrors the repository's clamping so the meta block reports the paging
// that was actually applied rather than what the client asked for.
func pagingOf(page, perPage int) (int, int) {
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

func totalPages(total int64, perPage int) int {
	if perPage < 1 || total == 0 {
		return 0
	}
	return int((total + int64(perPage) - 1) / int64(perPage))
}
