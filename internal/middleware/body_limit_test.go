package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// bodyLimitRouter's handler reads the whole body and reports whether
// reading was allowed.
func bodyLimitRouter(max int64, handlerRan *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(max))
	r.POST("/", func(c *gin.Context) {
		*handlerRan = true
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

func TestBodyLimit(t *testing.T) {
	t.Run("small body passes", func(t *testing.T) {
		var ran bool
		rec := httptest.NewRecorder()
		bodyLimitRouter(10, &ran).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345")))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("announced too-large body is refused before the handler", func(t *testing.T) {
		var ran bool
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))

		bodyLimitRouter(10, &ran).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"payload_too_large"`)
		assert.False(t, ran, "the body is never read")
	})

	t.Run("unannounced (chunked) too-large body is cut off while reading", func(t *testing.T) {
		var ran bool
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))
		req.ContentLength = -1 // unknown length, like Transfer-Encoding: chunked

		bodyLimitRouter(10, &ran).ServeHTTP(rec, req)

		assert.True(t, ran, "the fast path cannot help here")
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "reading past the limit fails")
	})
}
