package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeWebhookService struct {
	gotPayload, gotHeader string
	err                   error
}

func (f *fakeWebhookService) Handle(_ context.Context, payload []byte, header string) error {
	f.gotPayload, f.gotHeader = string(payload), header
	return f.err
}

func webhookRequest(svc WebhookService) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/webhooks/stripe", NewWebhookHandler(svc).Stripe)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/stripe", strings.NewReader(`{"id": "evt_1"}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=abc")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestWebhookHandler(t *testing.T) {
	svc := &fakeWebhookService{}
	rec := webhookRequest(svc)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"id": "evt_1"}`, svc.gotPayload, "the exact bytes, spaces included: they are what is signed")
	assert.Equal(t, "t=1,v1=abc", svc.gotHeader)
}

func TestWebhookHandler_Errors(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, webhookRequest(&fakeWebhookService{err: model.ErrWebhookSignature}).Code,
		"forged: 400, Stripe would not retry")
	assert.Equal(t, http.StatusInternalServerError, webhookRequest(&fakeWebhookService{err: errors.New("db down")}).Code,
		"our failure: 500, Stripe retries")
}
