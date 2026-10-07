package handler

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// WebhookService is what WebhookHandler needs from the service layer.
type WebhookService interface {
	Handle(ctx context.Context, payload []byte, signatureHeader string) error
}

type WebhookHandler struct {
	svc WebhookService
}

func NewWebhookHandler(svc WebhookService) *WebhookHandler {
	return &WebhookHandler{svc: svc}
}

// Stripe handles POST /api/v1/webhooks/stripe.
//
//	@Summary		Stripe webhook
//	@Description	Called by Stripe, not by clients. No token: the Stripe-Signature header proves the sender.
//	@Description	200 = handled (or not needed); 400 = not signed by Stripe; 500 = failed, Stripe retries for up to 3 days.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			Stripe-Signature	header	string	true	"Signature made by Stripe with the webhook secret"
//	@Success		200
//	@Failure		400	{object}	apierror.Response	"invalid signature"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/webhooks/stripe [post]
func (h *WebhookHandler) Stripe(c *gin.Context) {
	// The signature covers the exact bytes Stripe sent, so read them raw:
	// decoding and re-encoding the JSON would change them. BodyLimit
	// (1 MB) still applies.
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeBadRequest, "could not read the request body", nil)
		return
	}

	err = h.svc.Handle(c.Request.Context(), payload, c.GetHeader("Stripe-Signature"))
	switch {
	case errors.Is(err, model.ErrWebhookSignature):
		// Logged with the reason; the sender learns nothing more.
		_ = c.Error(err)
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeBadRequest, "invalid webhook signature", nil)
	case err != nil:
		handleError(c, err) // 500 (or 502): Stripe retries
	default:
		c.JSON(http.StatusOK, gin.H{"received": true})
	}
}
