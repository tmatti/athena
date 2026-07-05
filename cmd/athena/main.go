package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tmatti/athena/internal/api"
	"github.com/tmatti/athena/internal/config"
	"github.com/tmatti/athena/internal/db"
	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "athena:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.EnsureEmbeddingMeta(ctx, pool, cfg.EmbeddingProvider, cfg.EmbeddingModel, cfg.EmbeddingDimensions); err != nil {
		return err
	}
	log.Info("database ready")

	var embedder embed.Embedder
	if cfg.EmbeddingProvider == "openai_compatible" {
		embedder = embed.NewOpenAICompatible(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	} else {
		log.Info("embedding provider disabled; search runs keyword-only")
	}

	brain := service.New(store.New(pool), embedder, log)
	go brain.RunEmbedRetryLoop(ctx, time.Minute)
	handlers := &api.Handlers{Brain: brain}

	router := api.NewRouter(api.RouterOptions{
		Log:     log,
		APIKey:  cfg.BrainAPIKey,
		Healthy: pool.Ping,
		V1:      handlers.Routes,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
