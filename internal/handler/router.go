package handler

import (
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	// Blank import: its init() registers the generated spec with swag, so
	// the Swagger UI handler can serve it.
	_ "github.com/jaloldinov/ELD-learning/docs"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// RouterOptions turns optional router features on or off.
type RouterOptions struct {
	// EnableSwagger serves Swagger UI at /swagger/index.html.
	// Off in production: it would show the whole API surface to anyone.
	EnableSwagger bool

	// TrustedProxies: see config.Config.TrustedProxies. Must be validated
	// IPs/CIDRs (config.Load does this).
	TrustedProxies []string

	// HSTS sends Strict-Transport-Security. Only in production, where the
	// API is served over HTTPS; in development it would make browsers refuse
	// plain http://localhost.
	HSTS bool
}

// Handlers groups every HTTP handler the router needs.
// New handlers (company, user, truck, ...) are added here as we build them.
type Handlers struct {
	Health    *HealthHandler
	Auth      *AuthHandler
	User      *UserHandler
	Company   *CompanyHandler
	Driver    *DriverHandler
	Truck     *TruckHandler
	ELDDevice *ELDDeviceHandler
	RateLimit *RateLimitHandler
	Password  *PasswordResetHandler
}

// RouterSecurity holds what the router needs to protect routes.
type RouterSecurity struct {
	Tokens      middleware.TokenParser      // authentication: who is calling?
	Permissions middleware.PermissionLoader // authorization: what may they do?
	AuthLimiter middleware.RateLimiter      // per-IP limit on the public /auth routes
}

// NewRouter builds the Gin engine and registers all routes.
//
// It only connects URLs to middleware and handlers: no business rules and no
// database access belong here (those live in service and repository).
func NewRouter(log *slog.Logger, opts RouterOptions, sec RouterSecurity, h Handlers) *gin.Engine {
	r := gin.New()

	// c.ClientIP() believes X-Forwarded-For only when the request comes from
	// a trusted proxy. With none configured it uses the TCP peer address, so
	// clients cannot fake their IP (in logs, or to dodge rate limits).
	// Behind a load balancer, its address MUST be configured, or every
	// client shares the balancer's IP and one rate limit.
	if err := r.SetTrustedProxies(opts.TrustedProxies); err != nil {
		// config.Load validated the list, so this is a programming error.
		panic(fmt.Sprintf("invalid trusted proxies: %v", err))
	}

	// Order matters. Middleware wraps like an onion:
	//   RequestID → RequestLogger → Recovery → BodyLimit → handler
	// - RequestID is first, so every later step can read the ID.
	// - RequestLogger sits outside Recovery, so when Recovery turns a panic
	//   into a 500, the logger still sees and logs that 500.
	// - BodyLimit sits inside the logger, so refused (413) bodies are logged.
	r.Use(
		middleware.RequestID(),
		middleware.RequestLogger(log),
		middleware.Recovery(log),
		middleware.BodyLimit(middleware.MaxBodyBytes),
	)

	// Unknown routes return our JSON error format instead of Gin's plain text.
	r.NoRoute(NotFound)

	// Infrastructure endpoints: not versioned, never require auth.
	secHeaders := middleware.SecurityHeaders(opts.HSTS)
	r.GET("/health", secHeaders, h.Health.Live)
	r.GET("/ready", secHeaders, h.Health.Ready)

	if opts.EnableSwagger {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// Versioned API. A breaking change later becomes /api/v2 while v1 keeps
	// working for old clients (e.g. ELD apps that are not updated yet).
	v1 := r.Group("/api/v1", secHeaders)

	// ---------- Public: no token needed (you cannot need a token to get one) ----------
	// Rate limited per client IP: these are the routes attackers can call
	// without a token, and login runs an expensive bcrypt check.
	public := v1.Group("/auth", middleware.RateLimitByIP(sec.AuthLimiter))
	public.POST("/login", h.Auth.Login)
	public.POST("/refresh", h.Auth.Refresh) // the refresh token itself is the credential
	public.POST("/logout", h.Auth.Logout)
	public.POST("/password/forgot", h.Password.Forgot)
	public.POST("/password/verify", h.Password.Verify)
	public.POST("/password/reset", h.Password.Reset) // the reset token is the credential

	// ---------- Protected: every route below requires a valid access token ----------
	// Registering the middleware on the GROUP (not per route) means a new
	// route added here can never forget authentication.
	protected := v1.Group("", middleware.Authenticate(sec.Tokens))
	perm := middleware.NewPermissionChecker(sec.Permissions)

	// Only needs a login: everyone may ask "who am I?".
	protected.GET("/auth/me", h.User.Me)

	// Every other protected route declares the permission it needs, right
	// next to the route, so a reviewer sees "who may call this" at a glance.
	// Tenant scope ("only your own company") is enforced by the services.
	companies := protected.Group("/companies")
	companies.POST("", perm.Require(model.PermCompanyCreate), h.Company.Create)
	companies.GET("", perm.Require(model.PermCompanyRead), h.Company.List)
	companies.GET("/:id", perm.Require(model.PermCompanyRead), h.Company.Get)
	companies.PATCH("/:id", perm.Require(model.PermCompanyUpdate), h.Company.Update)
	companies.DELETE("/:id", perm.Require(model.PermCompanyDelete), h.Company.Delete)

	users := protected.Group("/users")
	users.POST("", perm.Require(model.PermUserCreate), h.User.Create)
	users.GET("", perm.Require(model.PermUserRead), h.User.List)
	users.GET("/:id", perm.Require(model.PermUserRead), h.User.Get)
	users.PATCH("/:id", perm.Require(model.PermUserUpdate), h.User.Update)
	users.DELETE("/:id", perm.Require(model.PermUserDelete), h.User.Delete)
	users.PUT("/:id/password", perm.Require(model.PermUserSetPassword), h.User.SetPassword)

	drivers := protected.Group("/drivers")
	// Login only: a driver reads their OWN profile (identity, not a permission).
	// Registered before /:id; Gin matches the static segment "me" first.
	drivers.GET("/me", h.Driver.Me)
	drivers.POST("", perm.Require(model.PermDriverCreate), h.Driver.Create)
	drivers.GET("", perm.Require(model.PermDriverRead), h.Driver.List)
	drivers.GET("/:id", perm.Require(model.PermDriverRead), h.Driver.Get)
	drivers.PATCH("/:id", perm.Require(model.PermDriverUpdate), h.Driver.Update)
	drivers.DELETE("/:id", perm.Require(model.PermDriverDelete), h.Driver.Delete)

	trucks := protected.Group("/trucks")
	trucks.POST("", perm.Require(model.PermTruckCreate), h.Truck.Create)
	trucks.GET("", perm.Require(model.PermTruckRead), h.Truck.List)
	trucks.GET("/:id", perm.Require(model.PermTruckRead), h.Truck.Get)
	trucks.PATCH("/:id", perm.Require(model.PermTruckUpdate), h.Truck.Update)
	trucks.DELETE("/:id", perm.Require(model.PermTruckDelete), h.Truck.Delete)

	// Installing/removing a device is an edit of the device: eld:update.
	elds := protected.Group("/eld-devices")
	elds.POST("", perm.Require(model.PermELDCreate), h.ELDDevice.Create)
	elds.GET("", perm.Require(model.PermELDRead), h.ELDDevice.List)
	elds.GET("/:id", perm.Require(model.PermELDRead), h.ELDDevice.Get)
	elds.PATCH("/:id", perm.Require(model.PermELDUpdate), h.ELDDevice.Update)
	elds.DELETE("/:id", perm.Require(model.PermELDDelete), h.ELDDevice.Delete)
	elds.PUT("/:id/truck", perm.Require(model.PermELDUpdate), h.ELDDevice.AssignTruck)
	elds.DELETE("/:id/truck", perm.Require(model.PermELDUpdate), h.ELDDevice.UnassignTruck)

	// Platform settings: permissions seeded for platform_admin only.
	rateLimits := protected.Group("/rate-limits")
	rateLimits.GET("", perm.Require(model.PermRateLimitRead), h.RateLimit.List)
	rateLimits.GET("/:name", perm.Require(model.PermRateLimitRead), h.RateLimit.Get)
	rateLimits.PATCH("/:name", perm.Require(model.PermRateLimitUpdate), h.RateLimit.Update)

	return r
}
