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
	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/config"
	"github.com/jaloldinov/ELD-learning/internal/database"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/notify"
	"github.com/jaloldinov/ELD-learning/internal/repository"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/internal/stripegw"
	"github.com/jaloldinov/ELD-learning/pkg/jobs"
	"github.com/jaloldinov/ELD-learning/pkg/mail"
	"github.com/jaloldinov/ELD-learning/pkg/ratelimit"
	"github.com/jaloldinov/ELD-learning/pkg/stripeclient"
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
//
//	@securityDefinitions.apikey	DeviceKey
//	@in							header
//	@name						Authorization
//	@description				ELD device key from POST /api/v1/eld-devices/{id}/credentials. Enter: Device <key>
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

	// Billing (Stripe) is optional, like email: without STRIPE_SECRET_KEY
	// the rest of the API works as before and the billing routes do not exist.
	var stripeClient *stripe.Client // nil = billing disabled
	if cfg.BillingEnabled() {
		stripeClient = stripeclient.New(stripeclient.Config{SecretKey: cfg.StripeSecretKey, Log: log})
		// Its own 5s: Stripe answers over the internet, unlike the database.
		stripeCtx, stripeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		acct, err := stripeclient.Check(stripeCtx, stripeClient)
		stripeCancel()
		switch {
		case errors.Is(err, stripeclient.ErrInvalidKey):
			// A wrong key will not fix itself: fail fast, like a bad config.
			return err
		case err != nil:
			// Stripe (or the network) may be down for a moment: the fleet
			// API must still start. Billing calls fail until it is back.
			log.Warn("stripe not reachable at startup; billing calls fail until it is", "error", err)
		default:
			log.Info("stripe connected", "account", acct.ID, "test_mode", cfg.StripeTestMode(),
				"webhooks", cfg.StripeWebhookSecret != "")
		}
	} else {
		log.Warn("STRIPE_SECRET_KEY not set; billing is disabled")
	}

	// Wire the 3 layers, bottom-up: repository → service → handler.
	// Each layer receives the one below it through a constructor (dependency
	// injection), so nothing reaches for globals and every layer is testable.

	// Transactions spanning several repositories (e.g. user + driver profile).
	txManager := repository.NewTxManager(pool)
	// Every service records its changes in the audit log, in the same
	// transaction as the change.
	auditRepo := repository.NewAuditRepository(pool)
	auditor := service.NewAuditor(txManager, auditRepo)

	companyRepo := repository.NewCompanyRepository(pool)
	companySvc := service.NewCompanyService(companyRepo, auditor)

	userRepo := repository.NewUserRepository(pool)
	hasher := auth.NewBcryptHasher(auth.DefaultBcryptCost)
	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	sessionRepo := repository.NewSessionRepository(pool)
	authSvc, err := service.NewAuthService(userRepo, sessionRepo, hasher, tokens, cfg.RefreshTokenTTL)
	if err != nil {
		return err
	}

	// The dispatch board: who drives which truck, which device is in it.
	// Built first: deleting or deactivating a user, truck or device takes it
	// off the board (UserService, TruckService, ELDDeviceService use it).
	driverRepo := repository.NewDriverRepository(pool)
	truckRepo := repository.NewTruckRepository(pool)
	eldRepo := repository.NewELDDeviceRepository(pool)
	eldStintRepo := repository.NewELDAssignmentRepository(pool)
	assignmentSvc := service.NewAssignmentService(service.AssignmentDeps{
		Tx:           txManager,
		DriverStints: repository.NewDriverAssignmentRepository(pool),
		ELDStints:    eldStintRepo,
		Drivers:      driverRepo,
		Devices:      eldRepo,
		Trucks:       truckRepo,
		Users:        userRepo,
		Audit:        auditor,
	})

	// The devices themselves (Authorization: Device <key>).
	deviceSvc := service.NewDeviceService(eldRepo, eldStintRepo, truckRepo, repository.NewTelemetryRepository(pool))

	userSvc := service.NewUserService(userRepo, assignmentSvc, companyRepo, sessionRepo, hasher, auditor)
	driverSvc := service.NewDriverService(txManager, userSvc, driverRepo, auditor)

	// Shared: TruckService manages trucks; ELDDeviceService looks them up.
	truckSvc := service.NewTruckService(truckRepo, assignmentSvc, companyRepo, auditor)

	// Background jobs run until jobsCtx is cancelled (when run returns).
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()

	// Rate limits for the public auth routes. The numbers live in the
	// rate_limits table and are changed through /api/v1/rate-limits by a
	// platform admin:
	//   - auth_requests: per IP on /auth/*.
	//   - login_attempts: per login+IP on /auth/login; a success resets it.
	// Startup fails if they cannot be loaded: better than running unprotected.
	rateLimitSvc := service.NewRateLimitService(repository.NewRateLimitRepository(pool), auditor)
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
	resetLocal, err := rateLimitSvc.Limiter(dbCtx, model.RateLimitPasswordReset)
	if err != nil {
		return err
	}
	go resetLocal.RunCleanup(jobsCtx)

	// Where attempts are COUNTED:
	//   - REDIS_URL set: in Redis, shared by all instances (3 servers still
	//     allow 5 attempts in total). If Redis fails, each instance falls back
	//     to its in-memory counter until Redis is back.
	//   - REDIS_URL empty: in memory, per instance. Only correct with one
	//     instance (local development).
	var authIPLimiter middleware.RateLimiter = authLocal
	var loginLimiter handler.LoginLimiter = loginLocal
	var resetLimiter handler.ResetRequestLimiter = resetLocal
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
		resetLimiter = ratelimit.NewRedis(rdb, model.RateLimitPasswordReset, resetLocal, log)
	} else {
		log.Warn("REDIS_URL not set; rate limits count in memory per instance (fine for a single instance only)")
	}

	// Every minute: re-read the rate limits, so a change saved through
	// another instance arrives here too. One tiny query per minute, instead
	// of one per login. If the database is down, the current values stay.
	go jobs.Every(jobsCtx, log, "rate-limit-refresh", time.Minute, rateLimitSvc.Refresh)

	// Hourly: delete refresh tokens that expired over 7 days ago. Safe to
	// run on every instance at once: deleting already-deleted rows is a no-op.
	// Email (password reset codes). Sent in the background, so a request
	// never waits for the mail server. Without SMTP_HOST, emails are only
	// logged (without their body) and "forgot password" cannot work.
	var mailer mail.Sender = mail.Disabled{Log: log}
	var mailQueue *mail.Async
	if cfg.SMTPHost != "" {
		smtpSender, err := mail.NewSMTP(mail.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, FromName: cfg.SMTPFromName,
		})
		if err != nil {
			return err
		}
		mailQueue = mail.NewAsync(smtpSender, log, 100, 15*time.Second)
		mailer = mailQueue
		log.Info("email enabled", "smtp_host", cfg.SMTPHost, "smtp_port", cfg.SMTPPort)
	} else {
		log.Warn("SMTP_HOST not set; emails are not sent (password reset will not work)")
	}

	// Notifications (OTP codes, security notices), routed by channel. A new
	// channel (e.g. Telegram) is one Transport registered here; services
	// do not change.
	notifier := notify.New().
		Register(notify.ChannelEmail, notify.NewEmail(mailer))

	resetRepo := repository.NewPasswordResetRepository(pool)
	passwordResetSvc := service.NewPasswordResetService(resetRepo, userRepo, sessionRepo, txManager,
		auth.NewResetCodeHasher(cfg.JWTSecret), hasher, notifier, auditor, log)

	// Hourly: delete password resets older than a day (they expire after
	// 10 minutes; a day keeps recent ones for investigating abuse).
	go jobs.Every(jobsCtx, log, "password-reset-cleanup", time.Hour, func(ctx context.Context) error {
		_, err := resetRepo.DeleteCreatedBefore(ctx, time.Now().Add(-24*time.Hour))
		return err
	})

	sessionCleaner := service.NewSessionCleaner(sessionRepo, 7*24*time.Hour)
	go jobs.Every(jobsCtx, log, "refresh-token-cleanup", time.Hour, func(ctx context.Context) error {
		n, err := sessionCleaner.Run(ctx)
		if n > 0 {
			log.Info("deleted expired refresh tokens", "count", n)
		}
		return err
	})

	// Billing handlers stay nil without Stripe: the router skips their routes.
	var (
		productHandler       *handler.ProductHandler
		paymentMethodHandler *handler.PaymentMethodHandler
		subscriptionHandler  *handler.SubscriptionHandler
		invoiceHandler       *handler.InvoiceHandler
		metricsHandler       *handler.BillingMetricsHandler
		webhookHandler       *handler.WebhookHandler
		billingGate          middleware.BillingStatusLoader // nil = everything free
	)
	if stripeClient != nil {
		productRepo := repository.NewProductRepository(pool)
		productHandler = handler.NewProductHandler(service.NewCatalogService(
			productRepo, stripegw.NewCatalog(stripeClient), auditor))
		customerSvc := service.NewCustomerService(companyRepo, stripegw.NewCustomers(stripeClient), auditor,
			cfg.StripePublishableKey)
		paymentMethodHandler = handler.NewPaymentMethodHandler(customerSvc)
		subscriptionGateway := stripegw.NewSubscriptions(stripeClient)
		subscriptionSvc := service.NewSubscriptionService(repository.NewSubscriptionRepository(pool), companyRepo,
			productRepo, truckRepo, customerSvc, subscriptionGateway, auditor)
		subscriptionHandler = handler.NewSubscriptionHandler(subscriptionSvc)
		invoiceSvc := service.NewInvoiceService(repository.NewInvoiceRepository(pool), companyRepo, customerSvc,
			stripegw.NewInvoices(stripeClient), subscriptionSvc, auditor)
		invoiceHandler = handler.NewInvoiceHandler(invoiceSvc)
		metricsHandler = handler.NewBillingMetricsHandler(service.NewBillingMetricsService(
			repository.NewBillingMetricsRepository(pool)))

		// Pay first, then use: fleet routes need a paid-up subscription.
		billingGate = companyRepo

		// Per-truck prices follow the fleet: after a truck is added, removed,
		// deactivated or reactivated, update the quantity in Stripe (the
		// difference goes on the next invoice). A failure here does not undo
		// the truck change; the hourly job below catches up.
		truckSvc.OnFleetChange(func(ctx context.Context, companyID uuid.UUID) {
			// Detached from the request: a client that hangs up must not
			// cancel the Stripe call halfway.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			if _, err := subscriptionSvc.SyncTruckQuantity(ctx, companyID); err != nil {
				log.Warn("truck count not synced to stripe; the hourly job will retry",
					"company_id", companyID, "error", err)
			}
		})
		go jobs.Every(jobsCtx, log, "truck-quantity-reconcile", time.Hour, func(ctx context.Context) error {
			n, err := subscriptionSvc.ReconcileTruckQuantities(ctx)
			if n > 0 {
				log.Info("truck counts synced to stripe", "subscriptions", n)
			}
			return err
		})

		if cfg.StripeWebhookSecret != "" {
			webhookHandler = handler.NewWebhookHandler(service.NewWebhookService(
				stripegw.NewWebhooks(cfg.StripeWebhookSecret), repository.NewStripeEventRepository(pool),
				subscriptionSvc, subscriptionGateway, invoiceSvc, log))
		} else {
			log.Warn("STRIPE_WEBHOOK_SECRET not set; Stripe webhooks are refused, so payments and renewals " +
				"reach us only through the refresh endpoint")
		}
	}

	router := handler.NewRouter(log, handler.RouterOptions{
		EnableSwagger:  !cfg.IsProduction(),
		TrustedProxies: cfg.TrustedProxies,
		HSTS:           cfg.IsProduction(),
	}, handler.RouterSecurity{
		Tokens:      tokens,
		Permissions: userSvc,
		AuthLimiter: authIPLimiter,
		Billing:     billingGate,
		Devices:     deviceSvc,
	}, handler.Handlers{
		Health:        handler.NewHealthHandler(pool, log),
		Auth:          handler.NewAuthHandler(authSvc, loginLimiter),
		User:          handler.NewUserHandler(userSvc),
		Company:       handler.NewCompanyHandler(companySvc),
		Driver:        handler.NewDriverHandler(driverSvc),
		Truck:         handler.NewTruckHandler(truckSvc),
		ELDDevice:     handler.NewELDDeviceHandler(service.NewELDDeviceService(eldRepo, assignmentSvc, companyRepo, auditor)),
		Device:        handler.NewDeviceHandler(deviceSvc),
		Assignment:    handler.NewAssignmentHandler(assignmentSvc),
		RateLimit:     handler.NewRateLimitHandler(rateLimitSvc),
		Password:      handler.NewPasswordResetHandler(passwordResetSvc, resetLimiter),
		AuditLog:      handler.NewAuditLogHandler(service.NewAuditLogService(auditRepo)),
		Product:       productHandler,
		PaymentMethod: paymentMethodHandler,
		Subscription:  subscriptionHandler,
		Webhook:       webhookHandler,
		Invoice:       invoiceHandler,
		Metrics:       metricsHandler,
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

	// After the server: requests may still have queued emails. Give the
	// queue a few seconds to deliver them (a code lost here can be
	// requested again).
	if mailQueue != nil {
		mailCtx, mailCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer mailCancel()
		if err := mailQueue.Close(mailCtx); err != nil {
			log.Warn("emails still queued at shutdown were dropped", "error", err)
		}
	}
	return nil
}
