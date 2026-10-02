package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/logger"
)

// fakeParser accepts exactly one token string: "good-token".
type fakeParser struct {
	claims *auth.TokenClaims
	gotTok string
}

func (f *fakeParser) Parse(token string) (*auth.TokenClaims, error) {
	f.gotTok = token
	if token != "good-token" {
		return nil, errors.New("token is expired")
	}
	return f.claims, nil
}

func newFakeParser() *fakeParser {
	companyID := uuid.New()
	return &fakeParser{claims: &auth.TokenClaims{UserID: uuid.New(), CompanyID: &companyID}}
}

// protectedRouter has one route behind Authenticate that reports what it saw.
func protectedRouter(p TokenParser, reached *bool, seen *auth.Principal) *gin.Engine {
	r := gin.New()
	r.Use(Authenticate(p))
	r.GET("/private", func(c *gin.Context) {
		*reached = true
		*seen, _ = auth.PrincipalFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	return r
}

func TestAuthenticate_ValidToken(t *testing.T) {
	parser := newFakeParser()
	var reached bool
	var seen auth.Principal

	for _, header := range []string{"Bearer good-token", "bearer good-token", "BEARER   good-token"} {
		reached = false
		rec := doRequest(protectedRouter(parser, &reached, &seen), http.MethodGet, "/private",
			map[string]string{"Authorization": header})

		assert.Equal(t, http.StatusOK, rec.Code, header)
		assert.True(t, reached, header)
		assert.Equal(t, parser.claims.UserID, seen.UserID, "principal comes from the token")
		assert.Equal(t, parser.claims.CompanyID, seen.CompanyID)
	}
}

func TestAuthenticate_Rejects(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		wantAuthHdr string
	}{
		{"no header", "", `Bearer realm="eld-api"`},
		{"wrong scheme", "Basic dXNlcjpwYXNz", `Bearer realm="eld-api"`},
		{"scheme only", "Bearer", `Bearer realm="eld-api"`},
		{"empty token", "Bearer    ", `Bearer realm="eld-api"`},
		{"token without scheme", "good-token", `Bearer realm="eld-api"`},
		{"invalid token", "Bearer bad-token", `Bearer realm="eld-api", error="invalid_token"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			var seen auth.Principal
			headers := map[string]string{}
			if tt.header != "" {
				headers["Authorization"] = tt.header
			}

			rec := doRequest(protectedRouter(newFakeParser(), &reached, &seen), http.MethodGet, "/private", headers)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, reached, "the handler must not run")
			assert.Equal(t, tt.wantAuthHdr, rec.Header().Get("WWW-Authenticate"))

			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, "unauthorized", body.Error.Code, "same error format as the rest of the API")
			assert.NotContains(t, rec.Body.String(), "token is expired",
				"the parser's internal reason is not sent to the client")
		})
	}
}

func TestRequestLogger_IncludesUserIDAndAuthErrors(t *testing.T) {
	parser := newFakeParser()
	var buf bytes.Buffer
	r := gin.New()
	r.Use(RequestLogger(logger.New(&buf, true, "debug")), Authenticate(parser))
	r.GET("/private", func(c *gin.Context) { c.Status(http.StatusOK) })

	t.Run("authenticated request logs user_id", func(t *testing.T) {
		buf.Reset()
		doRequest(r, http.MethodGet, "/private", map[string]string{"Authorization": "Bearer good-token"})

		lines := decodeLogLines(t, &buf)
		require.Len(t, lines, 1)
		assert.Equal(t, parser.claims.UserID.String(), lines[0]["user_id"])
	})

	t.Run("rejected token logs the reason", func(t *testing.T) {
		buf.Reset()
		doRequest(r, http.MethodGet, "/private", map[string]string{"Authorization": "Bearer bad-token"})

		lines := decodeLogLines(t, &buf)
		require.Len(t, lines, 1)
		assert.NotContains(t, lines[0], "user_id")
		assert.Contains(t, lines[0]["errors"], "token is expired")
	})
}
