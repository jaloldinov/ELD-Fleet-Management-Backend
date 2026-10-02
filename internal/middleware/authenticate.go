package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/handler"
)

// TokenParser verifies an access token (auth.TokenManager).
type TokenParser interface {
	Parse(token string) (*auth.TokenClaims, error)
}

// Authenticate answers "who is calling?". It requires a valid
// "Authorization: Bearer <access token>" header, and stores the caller as an
// auth.Principal in the request context for handlers and services.
//
// It does not touch the database: the token's signature and expiry are enough
// to trust its claims. What the caller may DO is checked later, by
// RequirePermission.
func Authenticate(tokens TokenParser) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			// RFC 6750: a 401 must say which auth scheme the API expects.
			c.Header("WWW-Authenticate", `Bearer realm="eld-api"`)
			handler.RespondError(c, http.StatusUnauthorized, handler.CodeUnauthorized,
				"missing or malformed Authorization header", nil)
			return
		}

		claims, err := tokens.Parse(token)
		if err != nil {
			// Expired, tampered, wrong algorithm... The client only learns
			// "invalid"; the exact reason goes to the request log.
			_ = c.Error(err)
			c.Header("WWW-Authenticate", `Bearer realm="eld-api", error="invalid_token"`)
			handler.RespondError(c, http.StatusUnauthorized, handler.CodeUnauthorized,
				"invalid or expired access token", nil)
			return
		}

		p := auth.Principal{UserID: claims.UserID, CompanyID: claims.CompanyID}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		c.Next()
	}
}

// bearerToken extracts the token from "Bearer <token>". The scheme name is
// case-insensitive (RFC 7235), the token itself is not.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
