package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/service"
)

// AuthService is what AuthHandler needs from the service layer.
type AuthService interface {
	Login(ctx context.Context, login, password string) (*service.LoginResult, error)
}

type LoginRequest struct {
	// Email or username.
	Login    string `json:"login" example:"admin@example.com"`
	Password string `json:"password" example:"correct horse battery staple"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	// Always "Bearer": send the token as "Authorization: Bearer <access_token>".
	TokenType string `json:"token_type" example:"Bearer"`
	// Seconds until the access token expires.
	ExpiresIn int `json:"expires_in" example:"900"`
}

type AuthHandler struct {
	svc AuthService
	now func() time.Time // replaceable in tests
}

func NewAuthHandler(svc AuthService) *AuthHandler {
	return &AuthHandler{svc: svc, now: time.Now}
}

// Login handles POST /api/v1/auth/login.
//
//	@Summary		Log in
//	@Description	Exchanges a login (email or username) and password for an access token.
//	@Description	Unknown user and wrong password return the same 401 on purpose.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		LoginRequest	true	"Credentials"
//	@Success		200		{object}	TokenResponse
//	@Failure		400		{object}	ErrorResponse	"invalid JSON"
//	@Failure		401		{object}	ErrorResponse	"invalid login or password"
//	@Failure		403		{object}	ErrorResponse	"account is disabled"
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if !bindJSON(c, &req) {
		return
	}

	res, err := h.svc.Login(c.Request.Context(), req.Login, req.Password)
	if err != nil {
		handleError(c, err)
		return
	}

	// Tokens must never be stored by browsers or proxies.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, TokenResponse{
		AccessToken: res.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int(res.ExpiresAt.Sub(h.now()).Seconds()),
	})
}
