// Package handlers implements the HTTP endpoints of the Library Inventory API.
package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"go-server/internal/httpx"
	"go-server/internal/middleware"
	"go-server/internal/repository"
)

// respondRepoError maps a repository sentinel error onto the right status code.
//
// Centralising it means an unrecognised error can never leak a driver message to
// a client: anything unmapped becomes a generic 500 and is logged in full.
func respondRepoError(c *gin.Context, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		httpx.Error(c, http.StatusNotFound, httpx.CodeNotFound, notFoundMsg)

	case errors.Is(err, repository.ErrDuplicateEmail):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"an account with this email already exists")

	case errors.Is(err, repository.ErrDuplicateISBN):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"a book with this ISBN is already in the catalogue")

	case errors.Is(err, repository.ErrNoCopies):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"all copies of this book are currently on loan")

	case errors.Is(err, repository.ErrAlreadyLoaned):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"you already have this book on loan")

	case errors.Is(err, repository.ErrLoanClosed):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict,
			"this loan has already been returned")

	case errors.Is(err, repository.ErrStockConflict):
		httpx.Error(c, http.StatusConflict, httpx.CodeConflict, err.Error())

	default:
		// Attach to the context so the access log records the cause, then tell
		// the client only that something broke.
		_ = c.Error(err)
		middleware.LoggerOf(c).Error("unhandled repository error", slog.Any("error", err))
		httpx.Error(c, http.StatusInternalServerError, httpx.CodeInternal,
			"an unexpected error occurred")
	}
}
