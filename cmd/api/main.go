package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// Embeds the IANA time-zone database in the binary, so time.LoadLocation
	// works even in minimal Docker images that have no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/config"
	"github.com/jaloldinov/ELD-learning/internal/database"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run builds every dependency and starts the server.
// Keeping the logic here (instead of in main) lets us return errors normally.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.IsProduction(), cfg.LogLevel)

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// Give the database 5 seconds to answer at startup, then give up.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	pool, err := database.NewPool(dbCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close() // runs last, after the HTTP server has shut down
	log.Info("database connected")

	router := server.NewRouter(log, server.Handlers{
		Health: handler.NewHealthHandler(pool, log),
	})

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second, // protects against slow-header (Slowloris) attacks
	}

	// ctx is cancelled when the process receives Ctrl+C (SIGINT) or SIGTERM
	// (what Docker/Kubernetes send when stopping a container).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server starting", "addr", srv.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting new connections and give in-flight
	// requests up to 10 seconds to finish.
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped")
	return nil
}
