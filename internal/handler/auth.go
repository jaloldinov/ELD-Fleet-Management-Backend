package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// maxUserAgentLen keeps a client from storing huge strings in our database.
const maxUserAgentLen = 255

// AuthService is what AuthHandler needs from the service layer.
type AuthService interface {
	Login(ctx context.Context, login, password string, client model.ClientInfo) (*service.TokenPair, error)
	Refresh(ctx context.Context, refreshToken string, client model.ClientInfo) (*service.TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
}

type LoginRequest struct {
	// Email or username.
	Login    string `json:"login" example:"admin@example.com"`
	Password string `json:"password" example:"correct horse battery staple"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" example:"q3Jx0yJ8Q0m1yG2ZzVb0bq3s9Xc4m5n6o7p8q9r0s1t"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	// Always "Bearer": send the token as "Authorization: Bearer <access_token>".
	TokenType string `json:"token_type" example:"Bearer"`
	// Seconds until the access token expires.
	ExpiresIn int `json:"expires_in" example:"900"`
	// Single-use: exchange it at /api/v1/auth/refresh for a new pair.
	RefreshToken string `json:"refresh_token" example:"q3Jx0yJ8Q0m1yG2ZzVb0bq3s9Xc4m5n6o7p8q9r0s1t"`
	// Seconds until the refresh token (the session) expires.
	RefreshExpiresIn int `json:"refresh_expires_in" example:"2592000"`
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
//	@Description	Exchanges a login (email or username) and password for an access token and a refresh token.
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

	pair, err := h.svc.Login(c.Request.Context(), req.Login, req.Password, clientInfo(c))
	if err != nil {
		handleError(c, err)
		return
	}
	h.respondTokens(c, pair)
}

// Refresh handles POST /api/v1/auth/refresh.
//
//	@Summary		Refresh tokens
//	@Description	Exchanges a refresh token for a new access + refresh token pair.
//	@Description	The presented refresh token stops working (rotation). Presenting it again is treated as theft and ends all sessions of the user.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		RefreshRequest	true	"Refresh token"
//	@Success		200		{object}	TokenResponse
//	@Failure		400		{object}	ErrorResponse	"invalid JSON"
//	@Failure		401		{object}	ErrorResponse	"invalid or expired refresh token"
//	@Failure		403		{object}	ErrorResponse	"account is disabled"
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if !bindJSON(c, &req) {
		return
	}

	pair, err := h.svc.Refresh(c.Request.Context(), req.RefreshToken, clientInfo(c))
	if err != nil {
		handleError(c, err)
		return
	}
	h.respondTokens(c, pair)
}

// Logout handles POST /api/v1/auth/logout.
//
//	@Summary		Log out
//	@Description	Ends the session of the given refresh token. Always 204, even for unknown tokens.
//	@Description	The access token stays valid until it expires (max 15 minutes); clients should discard it.
//	@Tags			auth
//	@Accept			json
//	@Param			body	body	RefreshRequest	true	"Refresh token of the session to end"
//	@Success		204		"No Content"
//	@Failure		400		{object}	ErrorResponse	"invalid JSON"
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	var req RefreshRequest
	if !bindJSON(c, &req) {
		return
	}

	if err := h.svc.Logout(c.Request.Context(), req.RefreshToken); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) respondTokens(c *gin.Context, p *service.TokenPair) {
	now := h.now()
	// Tokens must never be stored by browsers or proxies.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, TokenResponse{
		AccessToken:      p.AccessToken,
		TokenType:        "Bearer",
		ExpiresIn:        int(p.AccessExpiresAt.Sub(now).Seconds()),
		RefreshToken:     p.RefreshToken,
		RefreshExpiresIn: int(p.RefreshExpiresAt.Sub(now).Seconds()),
	})
}

func clientInfo(c *gin.Context) model.ClientInfo {
	ua := c.Request.UserAgent()
	if len(ua) > maxUserAgentLen {
		ua = ua[:maxUserAgentLen]
	}
	return model.ClientInfo{UserAgent: ua, IP: c.ClientIP()}
}
