package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bindTarget struct {
	Name    string     `json:"name"`
	Year    *int       `json:"year"`
	Active  bool       `json:"active"`
	TruckID *uuid.UUID `json:"truck_id"`
	Roles   []string   `json:"roles"`
}

// bindRouter echoes 200 when bindJSON succeeds. maxBody > 0 wraps the body
// in http.MaxBytesReader, like the BodyLimit middleware does.
func bindRouter(maxBody int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/", func(c *gin.Context) {
		if maxBody > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
		}
		var dst bindTarget
		if bindJSON(c, &dst) {
			c.Status(http.StatusOK)
		}
	})
	return r
}

func TestBindJSON(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantCode    string
		wantDetails map[string]string
		wantMessage string
	}{
		{name: "valid", body: `{"name":"a","year":2022,"truck_id":"` + testUserID.String() + `"}`, wantStatus: 200},
		{name: "unknown field (typo)", body: `{"nmae":"a"}`, wantStatus: 400, wantCode: CodeValidation,
			wantDetails: map[string]string{"nmae": "unknown field"}},
		{name: "number sent as string", body: `{"year":"2022"}`, wantStatus: 400, wantCode: CodeValidation,
			wantDetails: map[string]string{"year": "must be an integer"}},
		{name: "string sent as number", body: `{"name":5}`, wantStatus: 400, wantCode: CodeValidation,
			wantDetails: map[string]string{"name": "must be a string"}},
		{name: "bool", body: `{"active":"yes"}`, wantStatus: 400, wantCode: CodeValidation,
			wantDetails: map[string]string{"active": "must be true or false"}},
		{name: "uuid sent as number is described as a string", body: `{"truck_id":5}`, wantStatus: 400,
			wantCode: CodeValidation, wantDetails: map[string]string{"truck_id": "must be a string"}},
		{name: "array", body: `{"roles":"admin"}`, wantStatus: 400, wantCode: CodeValidation,
			wantDetails: map[string]string{"roles": "must be an array"}},
		{name: "invalid uuid", body: `{"truck_id":"abc"}`, wantStatus: 400, wantCode: CodeBadRequest,
			wantMessage: "invalid request body: invalid UUID length: 3"},
		{name: "empty body", body: ``, wantStatus: 400, wantCode: CodeBadRequest, wantMessage: "request body must not be empty"},
		{name: "broken JSON", body: `{"name":`, wantStatus: 400, wantCode: CodeBadRequest, wantMessage: "request body must be valid JSON"},
		{name: "syntax error", body: `{name:1}`, wantStatus: 400, wantCode: CodeBadRequest, wantMessage: "request body must be valid JSON"},
		{name: "two JSON values", body: `{"name":"a"}{"name":"b"}`, wantStatus: 400, wantCode: CodeBadRequest},
		{name: "trailing garbage", body: `{"name":"a"} x`, wantStatus: 400, wantCode: CodeBadRequest},
		{name: "trailing whitespace is fine", body: "{\"name\":\"a\"}\n  ", wantStatus: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(bindRouter(0), http.MethodPost, "/", tt.body)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == 200 {
				return
			}
			e := decodeError(t, rec)
			assert.Equal(t, tt.wantCode, e.Code)
			if tt.wantDetails != nil {
				assert.Equal(t, tt.wantDetails, e.Details)
			}
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, e.Message)
			}
		})
	}
}

func TestBindJSON_TooLarge(t *testing.T) {
	body := `{"name":"` + strings.Repeat("x", 100) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()

	bindRouter(50).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	e := decodeError(t, rec)
	assert.Equal(t, CodeTooLarge, e.Code)
	assert.Equal(t, "request body must be at most 50 bytes", e.Message)
}
