package middleware

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// fakeDevices knows one key, "good-key"; "retired-key" belongs to a retired
// device, "broken-key" makes the lookup fail.
type fakeDevices struct {
	device auth.Device
	gotKey string
}

func (f *fakeDevices) Authenticate(_ context.Context, key string) (*auth.Device, error) {
	f.gotKey = key
	switch key {
	case "good-key":
		return &f.device, nil
	case "retired-key":
		return nil, model.ErrDeviceRetired
	case "broken-key":
		return nil, errors.New("database is down")
	}
	return nil, model.ErrDeviceKeyInvalid
}

func deviceRouter(f DeviceAuthenticator, reached *bool, seen *auth.Device) *gin.Engine {
	r := gin.New()
	r.Use(DeviceAuth(f))
	r.GET("/device/me", func(c *gin.Context) {
		*reached = true
		*seen, _ = auth.DeviceFromContext(c.Request.Context())
		_, isUser := auth.PrincipalFromContext(c.Request.Context())
		if isUser {
			c.Status(http.StatusTeapot) // must never happen
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

func TestDeviceAuth_ValidKey(t *testing.T) {
	f := &fakeDevices{device: auth.Device{DeviceID: uuid.New(), CompanyID: uuid.New()}}
	for _, header := range []string{"Device good-key", "device good-key", "DEVICE   good-key"} {
		var reached bool
		var seen auth.Device

		rec := doRequest(deviceRouter(f, &reached, &seen), http.MethodGet, "/device/me", map[string]string{"Authorization": header})

		assert.Equal(t, http.StatusOK, rec.Code, header)
		assert.True(t, reached)
		assert.Equal(t, f.device, seen, "the device comes from its key")
		assert.Equal(t, "good-key", f.gotKey)
	}
}

func TestDeviceAuth_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"a user's bearer token", "Bearer good-key", http.StatusUnauthorized},
		{"scheme only", "Device", http.StatusUnauthorized},
		{"unknown key", "Device bad-key", http.StatusUnauthorized},
		{"retired device", "Device retired-key", http.StatusForbidden},
		{"lookup failed", "Device broken-key", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			var seen auth.Device
			h := map[string]string{}
			if tt.header != "" {
				h["Authorization"] = tt.header
			}

			rec := doRequest(deviceRouter(&fakeDevices{}, &reached, &seen), http.MethodGet, "/device/me", h)

			assert.Equal(t, tt.want, rec.Code)
			assert.False(t, reached, "the handler never runs")
			if tt.want == http.StatusUnauthorized {
				assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `Device realm="eld-device-api"`)
			}
		})
	}
}
