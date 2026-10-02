package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger checks that a dependency (the database) is reachable.
// *pgxpool.Pool satisfies it; tests use a fake.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthResponse is returned by the liveness check.
type HealthResponse struct {
	Status string `json:"status" example:"ok"`
}

// ReadyResponse is returned by the readiness check.
type ReadyResponse struct {
	Status   string `json:"status" example:"ok"`
	Database string `json:"database" example:"up"`
}

// HealthHandler answers health checks from load balancers, Docker,
// Kubernetes, uptime monitors, etc.
type HealthHandler struct {
	db  Pinger
	log *slog.Logger
}

func NewHealthHandler(db Pinger, log *slog.Logger) *HealthHandler {
	return &HealthHandler{db: db, log: log}
}

// Live (liveness) reports whether the process is running. It deliberately does
// NOT check the database: if it did, a short database outage would make the
// orchestrator restart every app instance, which does not help.
//
//	@Summary	Liveness check
//	@Tags		health
//	@Produce	json
//	@Success	200	{object}	HealthResponse
//	@Router		/health [get]
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{Status: "ok"})
}

// Ready (readiness) reports whether the app can serve traffic right now.
// While it returns 503, the load balancer stops sending requests here.
//
//	@Summary	Readiness check
//	@Tags		health
//	@Produce	json
//	@Success	200	{object}	ReadyResponse
//	@Failure	503	{object}	ReadyResponse
//	@Router		/ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		// Full error goes to the log; the client only learns which check failed.
		h.log.ErrorContext(ctx, "readiness check failed", "check", "database", "error", err)
		c.JSON(http.StatusServiceUnavailable, ReadyResponse{Status: "unavailable", Database: "down"})
		return
	}

	c.JSON(http.StatusOK, ReadyResponse{Status: "ok", Database: "up"})
}
