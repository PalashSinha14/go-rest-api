package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"go-server/internal/auth"
	"go-server/internal/config"
	"go-server/internal/httpx"
	"go-server/internal/middleware"
	"go-server/internal/models"
	"go-server/internal/repository"
)

// Deps is everything the router needs to build the API.
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	Tokens *auth.TokenManager
	Users  repository.UserRepository
	Books  repository.BookRepository
	Loans  repository.LoanRepository
	Mongo  *mongo.Client
}

// NewRouter builds the fully wired HTTP handler.
//
// The route table is the clearest statement of the API's access model: every
// endpoint's required scope is visible in one place, rather than being buried in
// a permission check partway down a handler.
func NewRouter(d Deps) *gin.Engine {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.RedirectTrailingSlash = false

	// Order matters: an id must exist before the logger can quote it, and
	// recovery must wrap everything after it.
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(d.Logger))
	r.Use(middleware.Recovery(d.Logger))

	r.NoRoute(func(c *gin.Context) {
		httpx.Error(c, http.StatusNotFound, httpx.CodeNotFound, "endpoint not found")
	})
	r.NoMethod(func(c *gin.Context) {
		httpx.Error(c, http.StatusMethodNotAllowed, httpx.CodeNotFound,
			"method not allowed for this endpoint")
	})

	authH := NewAuthHandler(d.Users, d.Tokens)
	bookH := NewBookHandler(d.Books)
	loanH := NewLoanHandler(d.Loans, d.Books, d.Config.LoanPeriod, d.Config.MaxActiveLoans)

	r.GET("/health", healthHandler(d.Mongo))

	// Shorthands for the two access scopes used below.
	authed := middleware.RequireAuth(d.Tokens)
	librarian := middleware.RequireRole(models.RoleLibrarian)

	v1 := r.Group("/api/v1")

	// Public: obtaining a token.
	a := v1.Group("/auth")
	{
		a.POST("/register", middleware.ValidateJSON[models.RegisterRequest](), authH.Register)
		a.POST("/login", middleware.ValidateJSON[models.LoginRequest](), authH.Login)
		a.GET("/me", authed, authH.Me)
	}

	// Any signed-in reader may browse; only librarians may change the catalogue.
	b := v1.Group("/books", authed)
	{
		b.GET("", bookH.List)
		b.GET("/:id", bookH.Get)
		b.POST("", librarian, middleware.ValidateJSON[models.CreateBookRequest](), bookH.Create)
		b.PUT("/:id", librarian, middleware.ValidateJSON[models.UpdateBookRequest](), bookH.Update)
		b.DELETE("/:id", librarian, bookH.Delete)
	}

	l := v1.Group("/loans", authed)
	{
		l.POST("/borrow", middleware.ValidateJSON[models.BorrowRequest](), loanH.Borrow)
		l.POST("/:id/return", loanH.Return)
		l.GET("/me", loanH.ListMine)
		l.GET("", librarian, loanH.ListAll)
	}

	u := v1.Group("/users", authed, librarian)
	{
		u.PATCH("/:id/role", middleware.ValidateJSON[models.UpdateRoleRequest](), authH.UpdateRole)
	}

	return r
}

// healthHandler reports liveness and database reachability, so an orchestrator
// can tell "process is up" apart from "process is up but cannot serve".
func healthHandler(client *mongo.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		status := http.StatusOK
		database := "ok"
		if err := client.Ping(ctx, nil); err != nil {
			status = http.StatusServiceUnavailable
			database = "unreachable"
			middleware.LoggerOf(c).Error("health check failed", slog.Any("error", err))
		}

		c.JSON(status, gin.H{
			"status": map[bool]string{true: "success", false: "error"}[status == http.StatusOK],
			"data": gin.H{
				"service":  "library-inventory-api",
				"database": database,
				"time":     time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
}
