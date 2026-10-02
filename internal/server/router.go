// Package server wires HTTP routes to handlers and middleware.
package server

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	// Blank import: its init() registers the generated spec with swag, so
	// the Swagger UI handler can serve it.
	_ "github.com/jaloldinov/ELD-learning/docs"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Options turns optional router features on or off.
type Options struct {
	// EnableSwagger serves Swagger UI at /swagger/index.html.
	// Off in production: it would show the whole API surface to anyone.
	EnableSwagger bool
}

// Handlers groups every HTTP handler the router needs.
// New handlers (company, user, truck, ...) are added here as we build them.
type Handlers struct {
	Health  *handler.HealthHandler
	Auth    *handler.AuthHandler
	User    *handler.UserHandler
	Company *handler.CompanyHandler
	Driver  *handler.DriverHandler
}

// Security holds what the router needs to protect routes.
type Security struct {
	Tokens      middleware.TokenParser      // authentication: who is calling?
	Permissions middleware.PermissionLoader // authorization: what may they do?
}

// NewRouter builds the Gin engine and registers all routes.
func NewRouter(log *slog.Logger, opts Options, sec Security, h Handlers) *gin.Engine {
	r := gin.New()

	// We are not behind a trusted proxy yet, so ignore X-Forwarded-For and use
	// the real TCP peer address for c.ClientIP(). Otherwise any client could
	// fake its IP in our logs. (SetTrustedProxies(nil) cannot fail.)
	_ = r.SetTrustedProxies(nil)

	// Order matters. Middleware wraps like an onion:
	//   RequestID → RequestLogger → Recovery → handler
	// - RequestID is first, so every later step can read the ID.
	// - RequestLogger sits outside Recovery, so when Recovery turns a panic
	//   into a 500, the logger still sees and logs that 500.
	r.Use(
		middleware.RequestID(),
		middleware.RequestLogger(log),
		middleware.Recovery(log),
	)

	// Unknown routes return our JSON error format instead of Gin's plain text.
	r.NoRoute(handler.NotFound)

	// Infrastructure endpoints: not versioned, never require auth.
	r.GET("/health", h.Health.Live)
	r.GET("/ready", h.Health.Ready)

	if opts.EnableSwagger {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// Versioned API. A breaking change later becomes /api/v2 while v1 keeps
	// working for old clients (e.g. ELD apps that are not updated yet).
	v1 := r.Group("/api/v1")

	// ---------- Public: no token needed (you cannot need a token to get one) ----------
	public := v1.Group("/auth")
	public.POST("/login", h.Auth.Login)
	public.POST("/refresh", h.Auth.Refresh) // the refresh token itself is the credential
	public.POST("/logout", h.Auth.Logout)

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

	drivers := protected.Group("/drivers")
	// Login only: a driver reads their OWN profile (identity, not a permission).
	// Registered before /:id; Gin matches the static segment "me" first.
	drivers.GET("/me", h.Driver.Me)
	drivers.POST("", perm.Require(model.PermDriverCreate), h.Driver.Create)
	drivers.GET("", perm.Require(model.PermDriverRead), h.Driver.List)
	drivers.GET("/:id", perm.Require(model.PermDriverRead), h.Driver.Get)
	drivers.PATCH("/:id", perm.Require(model.PermDriverUpdate), h.Driver.Update)
	drivers.DELETE("/:id", perm.Require(model.PermDriverDelete), h.Driver.Delete)

	return r
}
