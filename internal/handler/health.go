package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthHandler answers "is the service alive?" checks from load balancers,
// Docker, Kubernetes, uptime monitors, etc.
type HealthHandler struct{}

func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// Check returns 200 OK when the process is running.
// Later it will also ping the database.
func (h *HealthHandler) Check(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
