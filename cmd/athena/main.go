package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmatti/athena/internal/api"
	"github.com/tmatti/athena/internal/config"
	"github.com/tmatti/athena/internal/db"
	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/mcpserver"
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
	stdio := flag.Bool("stdio", false, "run the MCP server over stdio instead of starting the HTTP server")
	flag.Parse()

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
		if cfg.EmbeddingAPIKey == "" {
			log.Warn("EMBEDDING_PROVIDER=openai_compatible with no EMBEDDING_API_KEY set; " +
				"this is fine for keyless local endpoints (e.g. Ollama), but against hosted " +
				"providers every write's embedding will fail and be endlessly retried; " +
				"set EMBEDDING_PROVIDER=none for keyword-only mode if this is unintentional")
		}
		embedder = embed.NewOpenAICompatible(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	} else {
		log.Info("embedding provider disabled; search runs keyword-only")
	}

	brain := service.New(store.New(pool), embedder, log)
	go brain.RunEmbedRetryLoop(ctx, time.Minute)

	if *stdio {
		return mcpserver.New(brain).Run(ctx, &mcp.StdioTransport{})
	}

	handlers := &api.Handlers{Brain: brain, Log: log}

	router := api.NewRouter(api.RouterOptions{
		Log:     log,
		APIKey:  cfg.BrainAPIKey,
		Healthy: pool.Ping,
		V1:      handlers.Routes,
		Mounts:  map[string]http.Handler{"/mcp": mcpserver.HTTPHandler(brain)},
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
	// Always log to stderr: in --stdio mode stdout carries the MCP JSON-RPC
	// protocol stream, and log lines would corrupt it.
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
