package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// DeviceAuthenticator turns a device key into the calling device
// (service.DeviceService).
type DeviceAuthenticator interface {
	Authenticate(ctx context.Context, key string) (*auth.Device, error)
}

// DeviceAuth answers "which ELD device is calling?" for the /device
// endpoints. It requires "Authorization: Device <key>" and stores the
// device as an auth.Device in the request context.
//
// Unlike Authenticate (users), it asks the database on every request: a
// device key is not a signed token with the answer inside, it is a random
// secret that only means something once we look it up. That is also what
// makes revoking a key take effect at once.
//
// A user's Bearer token is refused here, and a device key is refused by
// Authenticate: the two kinds of caller never cross.
func DeviceAuth(devices DeviceAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := deviceKey(c.GetHeader("Authorization"))
		if !ok {
			c.Header("WWW-Authenticate", `Device realm="eld-device-api"`)
			apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized,
				`missing or malformed Authorization header (expected "Device <key>")`, nil)
			return
		}

		d, err := devices.Authenticate(c.Request.Context(), key)
		switch {
		case errors.Is(err, model.ErrDeviceKeyInvalid):
			c.Header("WWW-Authenticate", `Device realm="eld-device-api", error="invalid_token"`)
			apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, err.Error(), nil)
			return
		case errors.Is(err, model.ErrDeviceRetired):
			apierror.Respond(c, http.StatusForbidden, apierror.CodeForbidden, err.Error(), nil)
			return
		case err != nil:
			_ = c.Error(err)
			apierror.Respond(c, http.StatusInternalServerError, apierror.CodeInternal, "internal server error", nil)
			return
		}

		c.Request = c.Request.WithContext(auth.WithDevice(c.Request.Context(), *d))
		c.Next()
	}
}

// deviceKey extracts the key from "Device <key>" (scheme case-insensitive).
func deviceKey(header string) (string, bool) {
	scheme, key, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Device") {
		return "", false
	}
	key = strings.TrimSpace(key)
	return key, key != ""
}
