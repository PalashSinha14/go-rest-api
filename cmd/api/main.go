// Command api runs the Library Inventory HTTP service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go-server/internal/auth"
	"go-server/internal/config"
	"go-server/internal/db"
	"go-server/internal/handlers"
	"go-server/internal/middleware"
	"go-server/internal/repository"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// run holds the real body of main so that every failure path returns an error
// rather than calling os.Exit, which would skip the deferred cleanup below.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	log.Info("starting library inventory api",
		slog.String("env", cfg.Env),
		slog.String("port", cfg.Port),
		slog.String("database", cfg.MongoDatabase),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mongoDB, err := db.Connect(ctx, cfg.MongoURI, cfg.MongoDatabase)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mongoDB.Disconnect(shutdownCtx); err != nil {
			log.Error("mongo disconnect failed", slog.Any("error", err))
		}
	}()
	log.Info("connected to mongodb")

	if err := db.EnsureIndexes(ctx, mongoDB.Database, log); err != nil {
		return err
	}

	if err := middleware.RegisterValidators(); err != nil {
		return err
	}

	router := handlers.NewRouter(handlers.Deps{
		Config: cfg,
		Logger: log,
		Tokens: auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL),
		Users:  repository.NewUserRepository(mongoDB.Database),
		Books:  repository.NewBookRepository(mongoDB.Database),
		Loans:  repository.NewLoanRepository(mongoDB.Database),
		Mongo:  mongoDB.Client,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// Wait for either a fatal listen error or a shutdown signal.
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}

	// Give in-flight requests a bounded window to finish before dropping them,
	// so a deploy does not sever a borrow midway through.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// newLogger builds the structured logger: JSON in production for log aggregators,
// human-readable text locally.
func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}

	var handler slog.Handler
	if cfg.IsProduction() {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler).With(slog.String("service", "library-inventory-api"))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
