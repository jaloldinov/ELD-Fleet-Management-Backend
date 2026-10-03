package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
)

// MaxBodyBytes is the largest request body the API accepts. Our biggest
// JSON request is a few hundred bytes; 1 MB leaves plenty of room.
const MaxBodyBytes int64 = 1 << 20

// BodyLimit caps every request body at max bytes, so a client cannot make
// the server read (and hold in memory) an arbitrarily large body.
func BodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Fast path: the client announced a too-large body. Refuse before
		// reading any of it.
		if c.Request.ContentLength > max {
			apierror.Respond(c, http.StatusRequestEntityTooLarge, apierror.CodeTooLarge,
				"request body must be at most "+strconv.FormatInt(max, 10)+" bytes", nil)
			return
		}
		// Content-Length can be missing (chunked) or wrong, so also cap the
		// actual reading. Reading past max fails with *http.MaxBytesError,
		// which bindJSON turns into the same 413.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}
