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
	Company *handler.CompanyHandler
}

// NewRouter builds the Gin engine and registers all routes.
func NewRouter(log *slog.Logger, opts Options, h Handlers) *gin.Engine {
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

	// Public: no token needed to obtain a token.
	authGroup := v1.Group("/auth")
	authGroup.POST("/login", h.Auth.Login)
	authGroup.POST("/refresh", h.Auth.Refresh)
	authGroup.POST("/logout", h.Auth.Logout)

	// TODO(auth step): only platform admins may manage companies.
	companies := v1.Group("/companies")
	companies.POST("", h.Company.Create)
	companies.GET("", h.Company.List)
	companies.GET("/:id", h.Company.Get)
	companies.PATCH("/:id", h.Company.Update)
	companies.DELETE("/:id", h.Company.Delete)

	return r
}
