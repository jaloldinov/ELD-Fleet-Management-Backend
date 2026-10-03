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
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/ratelimit"
	"github.com/jaloldinov/ELD-learning/internal/repository"
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

	// Roles and permissions are defined in the code (model.Permissions,
	// model.SystemRoles); make the database match before serving requests.
	// Only adds, never removes; see repository.RBACRepository.Sync.
	if err := service.SyncRBAC(dbCtx, repository.NewRBACRepository(pool), log); err != nil {
		return err
	}

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

	// Rate limits for the public auth routes. The numbers live in the
	// rate_limits table and are changed through /api/v1/rate-limits by a
	// platform admin:
	//   - auth_requests: per IP on /auth/*.
	//   - login_attempts: per login+IP on /auth/login; a success resets it.
	// Startup fails if they cannot be loaded: better than running unprotected.
	rateLimitSvc := service.NewRateLimitService(repository.NewRateLimitRepository(pool))
	authLocal, err := rateLimitSvc.Limiter(dbCtx, model.RateLimitAuthRequests)
	if err != nil {
		return err
	}
	loginLocal, err := rateLimitSvc.Limiter(dbCtx, model.RateLimitLoginAttempts)
	if err != nil {
		return err
	}
	go authLocal.RunCleanup(jobsCtx)
	go loginLocal.RunCleanup(jobsCtx)

	// Where attempts are COUNTED:
	//   - REDIS_URL set: in Redis, shared by all instances (3 servers still
	//     allow 5 attempts in total). If Redis fails, each instance falls back
	//     to its in-memory counter until Redis is back.
	//   - REDIS_URL empty: in memory, per instance. Only correct with one
	//     instance (local development).
	var authIPLimiter middleware.RateLimiter = authLocal
	var loginLimiter handler.LoginLimiter = loginLocal
	if cfg.RedisURL != "" {
		rdb, err := database.NewRedis(cfg.RedisURL, log)
		if err != nil {
			return err
		}
		defer rdb.Close()
		// Not fatal: the limiters fall back to memory and log the outage.
		if err := rdb.Ping(dbCtx).Err(); err != nil {
			log.Warn("redis not reachable at startup; rate limits count in memory until it is", "error", err)
		} else {
			log.Info("redis connected; rate limits are shared by all instances")
		}
		authIPLimiter = ratelimit.NewRedis(rdb, model.RateLimitAuthRequests, authLocal, log)
		loginLimiter = ratelimit.NewRedis(rdb, model.RateLimitLoginAttempts, loginLocal, log)
	} else {
		log.Warn("REDIS_URL not set; rate limits count in memory per instance (fine for a single instance only)")
	}

	// Every minute: re-read the rate limits, so a change saved through
	// another instance arrives here too. One tiny query per minute, instead
	// of one per login. If the database is down, the current values stay.
	go jobs.Every(jobsCtx, log, "rate-limit-refresh", time.Minute, rateLimitSvc.Refresh)

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

	router := handler.NewRouter(log, handler.RouterOptions{
		EnableSwagger:  !cfg.IsProduction(),
		TrustedProxies: cfg.TrustedProxies,
		HSTS:           cfg.IsProduction(),
	}, handler.RouterSecurity{
		Tokens:      tokens,
		Permissions: userSvc,
		AuthLimiter: authIPLimiter,
	}, handler.Handlers{
		Health:    handler.NewHealthHandler(pool, log),
		Auth:      handler.NewAuthHandler(authSvc, loginLimiter),
		User:      handler.NewUserHandler(userSvc),
		Company:   handler.NewCompanyHandler(companySvc),
		Driver:    handler.NewDriverHandler(driverSvc),
		Truck:     handler.NewTruckHandler(service.NewTruckService(truckRepo, companyRepo)),
		ELDDevice: handler.NewELDDeviceHandler(service.NewELDDeviceService(repository.NewELDDeviceRepository(pool), truckRepo, companyRepo)),
		RateLimit: handler.NewRateLimitHandler(rateLimitSvc),
	})

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second, // protects against slow-header (Slowloris) attacks
		// The whole request (headers + body) must arrive within 15s, so a
		// client cannot hold a connection by dripping its body byte by byte.
		ReadTimeout: 15 * time.Second,
		// Handler + writing the response. Generous for our short requests;
		// a request that needs longer is a bug or belongs in a background job.
		WriteTimeout: 30 * time.Second,
		// Keep-alive connections idle longer than this are closed.
		IdleTimeout: 60 * time.Second,
		// Default is 1 MB; our headers are a JWT and a few short values.
		MaxHeaderBytes: 64 << 10,
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
