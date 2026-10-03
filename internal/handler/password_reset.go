package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
)

// PasswordResetService is what PasswordResetHandler needs from the service
// layer.
type PasswordResetService interface {
	RequestReset(ctx context.Context, email string) error
	VerifyCode(ctx context.Context, email, code string) (token string, expiresIn time.Duration, err error)
	ResetPassword(ctx context.Context, token, newPassword string) error
}

// ResetRequestLimiter limits reset emails per address
// (ratelimit.Keyed or ratelimit.Redis).
type ResetRequestLimiter interface {
	Take(ctx context.Context, key string) (retryAfter time.Duration, ok bool)
}

// ---------- DTOs ----------

type ForgotPasswordRequest struct {
	Email string `json:"email" example:"jane@acme.com"`
}

type ForgotPasswordResponse struct {
	Message string `json:"message" example:"If an account with this email exists, a reset code has been sent."`
}

type VerifyResetCodeRequest struct {
	Email string `json:"email" example:"jane@acme.com"`
	// The 6-digit code from the email.
	Code string `json:"code" example:"048213"`
}

type VerifyResetCodeResponse struct {
	// Single use: send it to /api/v1/auth/password/reset.
	ResetToken string `json:"reset_token" example:"pX3k9Q0m1yG2ZzVb0bq3s9Xc4m5n6o7p8q9r0s1t2u3"`
	// Seconds until the reset token expires.
	ExpiresIn int `json:"expires_in" example:"600"`
}

type ResetPasswordRequest struct {
	ResetToken  string `json:"reset_token" example:"pX3k9Q0m1yG2ZzVb0bq3s9Xc4m5n6o7p8q9r0s1t2u3"`
	NewPassword string `json:"new_password" example:"correct horse battery staple"`
}

// forgotPasswordMessage is the same for every address, existing or not.
const forgotPasswordMessage = "If an account with this email exists, a reset code has been sent."

// ---------- Handler ----------

type PasswordResetHandler struct {
	svc     PasswordResetService
	limiter ResetRequestLimiter
}

func NewPasswordResetHandler(svc PasswordResetService, limiter ResetRequestLimiter) *PasswordResetHandler {
	return &PasswordResetHandler{svc: svc, limiter: limiter}
}

// Forgot handles POST /api/v1/auth/password/forgot.
//
//	@Summary		Forgot password: email a code
//	@Description	Step 1 of 3. Emails a 6-digit code (valid 10 minutes, 5 attempts) if the address belongs to an active account.
//	@Description	Always answers 202 with the same message, so it never reveals whether an account exists.
//	@Description	Users without an email address (username-only drivers) must ask an administrator.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ForgotPasswordRequest	true	"Account email"
//	@Success		202		{object}	ForgotPasswordResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or email"
//	@Failure		429		{object}	apierror.Response	"too many requests (rate limits password_reset_requests or auth_requests); see Retry-After"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/auth/password/forgot [post]
func (h *PasswordResetHandler) Forgot(c *gin.Context) {
	var req ForgotPasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	// Per address, so nobody can flood someone's inbox (the per-IP limit
	// on /auth also applies).
	key := strings.ToLower(strings.TrimSpace(req.Email))
	if retryAfter, ok := h.limiter.Take(c.Request.Context(), key); !ok {
		apierror.RespondRateLimited(c, retryAfter)
		return
	}
	if err := h.svc.RequestReset(c.Request.Context(), req.Email); err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, ForgotPasswordResponse{Message: forgotPasswordMessage})
}

// Verify handles POST /api/v1/auth/password/verify.
//
//	@Summary		Forgot password: verify the code
//	@Description	Step 2 of 3. Exchanges the emailed code for a single-use reset token (valid 10 minutes).
//	@Description	Each wrong code uses one of the 5 attempts; then a new code must be requested.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		VerifyResetCodeRequest	true	"Email and code"
//	@Success		200		{object}	VerifyResetCodeResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON, or invalid_reset_code (wrong, expired or used up)"
//	@Failure		429		{object}	apierror.Response	"too many requests from this IP; see Retry-After"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/auth/password/verify [post]
func (h *PasswordResetHandler) Verify(c *gin.Context) {
	var req VerifyResetCodeRequest
	if !bindJSON(c, &req) {
		return
	}
	token, expiresIn, err := h.svc.VerifyCode(c.Request.Context(), req.Email, req.Code)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, VerifyResetCodeResponse{ResetToken: token, ExpiresIn: int(expiresIn / time.Second)})
}

// Reset handles POST /api/v1/auth/password/reset.
//
//	@Summary		Forgot password: set a new password
//	@Description	Step 3 of 3. Sets the new password, ends every session of the user (log in again) and emails a notice.
//	@Tags			auth
//	@Accept			json
//	@Param			body	body	ResetPasswordRequest	true	"Reset token and new password"
//	@Success		204		"No Content"
//	@Failure		400		{object}	apierror.Response	"invalid JSON, weak new_password, or invalid_reset_token"
//	@Failure		429		{object}	apierror.Response	"too many requests from this IP; see Retry-After"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/auth/password/reset [post]
func (h *PasswordResetHandler) Reset(c *gin.Context) {
	var req ResetPasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.svc.ResetPassword(c.Request.Context(), req.ResetToken, req.NewPassword); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
