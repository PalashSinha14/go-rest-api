package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/httpx"
	"go-server/internal/middleware"
	"go-server/internal/models"
	"go-server/internal/repository"
)

// LoanHandler serves borrowing, returning and loan history.
type LoanHandler struct {
	loans          repository.LoanRepository
	books          repository.BookRepository
	loanPeriod     time.Duration
	maxActiveLoans int
	now            func() time.Time // injectable so tests can control due dates
}

// NewLoanHandler wires a LoanHandler to its repositories and lending policy.
func NewLoanHandler(
	loans repository.LoanRepository,
	books repository.BookRepository,
	loanPeriod time.Duration,
	maxActiveLoans int,
) *LoanHandler {
	return &LoanHandler{
		loans:          loans,
		books:          books,
		loanPeriod:     loanPeriod,
		maxActiveLoans: maxActiveLoans,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// Borrow checks out one copy of a book to the calling user.
//
// The ordering matters. Cheap policy checks come first, then the stock
// reservation, then the loan record — so the expensive, mutating step is only
// reached once the request is known to be allowed.
func (h *LoanHandler) Borrow(c *gin.Context) {
	userID, ok := middleware.UserObjectIDOf(c)
	if !ok {
		httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"authentication required")
		return
	}

	req := middleware.Payload[models.BorrowRequest](c)
	bookID, err := bson.ObjectIDFromHex(req.BookID)
	if err != nil {
		httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
			"book_id is not a valid id")
		return
	}

	ctx := c.Request.Context()
	log := middleware.LoggerOf(c)

	active, err := h.loans.CountActiveByUser(ctx, userID)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}
	if active >= int64(h.maxActiveLoans) {
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"you have reached the maximum number of books on loan")
		return
	}

	// Holding two copies of the same title helps nobody and hides a UI bug where
	// a borrow button is double-clicked.
	held, err := h.loans.HasActiveLoan(ctx, userID, bookID)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}
	if held {
		respondRepoError(c, repository.ErrAlreadyLoaned, "")
		return
	}

	// Atomically takes the copy; fails with ErrNoCopies if the shelf is empty.
	book, err := h.books.ReserveCopy(ctx, bookID)
	if err != nil {
		respondRepoError(c, err, "book not found")
		return
	}

	borrowedAt := h.now()
	loan := &models.Loan{
		BookID:     bookID,
		UserID:     userID,
		Status:     models.LoanActive,
		BorrowedAt: borrowedAt,
		DueAt:      borrowedAt.Add(h.loanPeriod),
	}

	if err := h.loans.Create(ctx, loan); err != nil {
		// The copy is already reserved, so failing here would strand it as
		// permanently unavailable. A standalone mongod has no multi-document
		// transaction to roll back with, so compensate explicitly instead.
		if relErr := h.books.ReleaseCopy(ctx, bookID); relErr != nil {
			log.Error("failed to release copy after loan creation failed",
				slog.String("book_id", bookID.Hex()),
				slog.Any("error", relErr),
			)
		}
		respondRepoError(c, err, "")
		return
	}

	log.Info("book borrowed",
		slog.String("loan_id", loan.ID.Hex()),
		slog.String("book_id", bookID.Hex()),
		slog.Int("copies_left", book.AvailableCopies),
	)

	loan.Book = book
	httpx.Success(c, http.StatusCreated, models.NewLoanView(loan, h.now()))
}

// Return closes a loan and puts the copy back on the shelf.
func (h *LoanHandler) Return(c *gin.Context) {
	userID, ok := middleware.UserObjectIDOf(c)
	if !ok {
		httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"authentication required")
		return
	}

	loanID, ok := objectIDParam(c, "id", "loan id")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	log := middleware.LoggerOf(c)

	loan, err := h.loans.FindByID(ctx, loanID)
	if err != nil {
		respondRepoError(c, err, "loan not found")
		return
	}

	// A member may only return their own loan; a librarian may return any, since
	// books come back over the desk rather than through the borrower's account.
	if loan.UserID != userID && !middleware.IsLibrarian(c) {
		httpx.Error(c, http.StatusForbidden, httpx.CodeForbidden,
			"this loan belongs to another member")
		return
	}

	// Guarded on status=="active", so a replayed request cannot restore stock twice.
	returned, err := h.loans.MarkReturned(ctx, loanID, h.now())
	if err != nil {
		respondRepoError(c, err, "loan not found")
		return
	}

	if err := h.books.ReleaseCopy(ctx, returned.BookID); err != nil {
		// The loan is already closed and that is the record of truth. Log loudly
		// rather than failing the caller, who has physically handed the book back.
		log.Error("loan closed but stock not restored",
			slog.String("loan_id", loanID.Hex()),
			slog.String("book_id", returned.BookID.Hex()),
			slog.Any("error", err),
		)
	}

	book, err := h.books.FindByID(ctx, returned.BookID)
	if err == nil {
		returned.Book = book
	}

	log.Info("book returned",
		slog.String("loan_id", returned.ID.Hex()),
		slog.String("book_id", returned.BookID.Hex()),
	)
	httpx.Success(c, http.StatusOK, models.NewLoanView(returned, h.now()))
}

// ListMine returns the calling user's own borrowing history.
func (h *LoanHandler) ListMine(c *gin.Context) {
	userID, ok := middleware.UserObjectIDOf(c)
	if !ok {
		httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"authentication required")
		return
	}
	h.list(c, &userID)
}

// ListAll returns every loan in the system. Librarian-only.
func (h *LoanHandler) ListAll(c *gin.Context) {
	var userID *bson.ObjectID
	if raw := c.Query("user_id"); raw != "" {
		id, err := bson.ObjectIDFromHex(raw)
		if err != nil {
			httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
				"user_id is not a valid id")
			return
		}
		userID = &id
	}
	h.list(c, userID)
}

// list is the shared body of the two listing endpoints. Scoping is decided by the
// caller rather than read from the query string, so a member cannot widen their
// own view by passing a different user_id.
func (h *LoanHandler) list(c *gin.Context, userID *bson.ObjectID) {
	filter := models.LoanFilter{
		UserID:  userID,
		Status:  models.LoanStatus(c.Query("status")),
		Overdue: c.Query("overdue") == "true",
		Page:    queryInt(c, "page", 1),
		PerPage: queryInt(c, "per_page", 20),
	}

	if filter.Status != "" &&
		filter.Status != models.LoanActive &&
		filter.Status != models.LoanReturned {
		httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
			`status must be "active" or "returned"`)
		return
	}

	loans, total, err := h.loans.List(c.Request.Context(), filter)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}

	now := h.now()
	views := make([]models.LoanView, 0, len(loans))
	for _, l := range loans {
		views = append(views, models.NewLoanView(l, now))
	}

	page, perPage := pagingOf(filter.Page, filter.PerPage)
	httpx.SuccessWithMeta(c, http.StatusOK, views, httpx.Meta{
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages(total, perPage),
	})
}
