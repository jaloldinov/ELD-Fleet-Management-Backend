// Package server wires HTTP routes to handlers and middleware.
package server

import (
	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/handler"
)

// Handlers groups every HTTP handler the router needs.
// New handlers (company, user, truck, ...) are added here as we build them.
type Handlers struct {
	Health *handler.HealthHandler
}

// NewRouter builds the Gin engine and registers all routes.
func NewRouter(h Handlers) *gin.Engine {
	// gin.New() starts with no middleware. gin.Default() would add Gin's own
	// text logger; we will replace it with our slog request logger.
	r := gin.New()
	r.Use(gin.Recovery()) // turns a panic into a 500 instead of crashing the process

	r.GET("/health", h.Health.Check)

	return r
}
