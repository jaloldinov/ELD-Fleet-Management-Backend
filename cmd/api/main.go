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

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/config"
	"github.com/jaloldinov/ELD-learning/internal/database"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/jobs"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/ratelimit"
	"github.com/jaloldinov/ELD-learning/internal/repository"
	"github.com/jaloldinov/ELD-learning/internal/server"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// General API information for Swagger (read by `make swagger`).
//
//	@title						ELD Fleet Management API
//	@version					1.0
//	@description				Backend for US motor carriers: companies, users, drivers, trucks and ELD devices.
//	@BasePath					/
//
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Access token from /api/v1/auth/login. Enter: Bearer <access_token>
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

	// Wire the 3 layers, bottom-up: repository → service → handler.
	// Each layer receives the one below it through a constructor (dependency
	// injection), so nothing reaches for globals and every layer is testable.
	companyRepo := repository.NewCompanyRepository(pool)
	companySvc := service.NewCompanyService(companyRepo)

	userRepo := repository.NewUserRepository(pool)
	hasher := auth.NewBcryptHasher(auth.DefaultBcryptCost)
	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	sessionRepo := repository.NewSessionRepository(pool)
	authSvc, err := service.NewAuthService(userRepo, sessionRepo, hasher, tokens, cfg.RefreshTokenTTL)
	if err != nil {
		return err
	}

	userSvc := service.NewUserService(userRepo, companyRepo, sessionRepo, hasher)

	// Transactions spanning several repositories (e.g. user + driver profile).
	txManager := repository.NewTxManager(pool)
	driverSvc := service.NewDriverService(txManager, userSvc, repository.NewDriverRepository(pool))

	// Shared: TruckService manages trucks; ELDDeviceService looks them up.
	truckRepo := repository.NewTruckRepository(pool)

	// Background jobs run until jobsCtx is cancelled (when run returns).
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()

	// Rate limits for the public auth routes (in memory, per instance):
	//   - per IP: 20 requests/min on /auth/*. Generous enough for a carrier
	//     office where many drivers share one NAT address at shift start.
	//   - per login+IP: 5 login attempts per 15 min; a success resets it.
	authIPLimiter := ratelimit.New(20, time.Minute)
	loginLimiter := ratelimit.New(5, 15*time.Minute)
	go authIPLimiter.RunCleanup(jobsCtx)
	go loginLimiter.RunCleanup(jobsCtx)

	// Hourly: delete refresh tokens that expired over 7 days ago. Safe to
	// run on every instance at once: deleting already-deleted rows is a no-op.
	sessionCleaner := service.NewSessionCleaner(sessionRepo, 7*24*time.Hour)
	go jobs.Every(jobsCtx, log, "refresh-token-cleanup", time.Hour, func(ctx context.Context) error {
		n, err := sessionCleaner.Run(ctx)
		if n > 0 {
			log.Info("deleted expired refresh tokens", "count", n)
		}
		return err
	})

	router := server.NewRouter(log, server.Options{EnableSwagger: !cfg.IsProduction()}, server.Security{
		Tokens:      tokens,
		Permissions: userSvc,
		AuthLimiter: authIPLimiter,
	}, server.Handlers{
		Health:    handler.NewHealthHandler(pool, log),
		Auth:      handler.NewAuthHandler(authSvc, loginLimiter),
		User:      handler.NewUserHandler(userSvc),
		Company:   handler.NewCompanyHandler(companySvc),
		Driver:    handler.NewDriverHandler(driverSvc),
		Truck:     handler.NewTruckHandler(service.NewTruckService(truckRepo, companyRepo)),
		ELDDevice: handler.NewELDDeviceHandler(service.NewELDDeviceService(repository.NewELDDeviceRepository(pool), truckRepo, companyRepo)),
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
