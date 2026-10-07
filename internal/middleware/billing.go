package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// BillingStatusHeader tells the client the company's billing status on
// every gated response, so a page can show "your payment failed" while
// the company is still in its grace period.
const BillingStatusHeader = "X-Billing-Status"

// BillingStatusLoader returns a company's billing status
// (repository.CompanyRepository.BillingStatus).
type BillingStatusLoader interface {
	BillingStatus(ctx context.Context, companyID uuid.UUID) (string, error)
}

// RequireActiveBilling lets company users through only while their
// company's subscription allows using the product (active, or past_due
// during the grace period). Must run after Authenticate.
//
//   - platform users            → always through (they are not billed)
//   - status cannot be loaded   → 500 (fail closed: never free by accident)
//   - none / pending / blocked  → 402 Payment Required
//
// It guards the product (fleet routes), never the billing routes: a
// blocked company must still be able to add a card and pay.
func RequireActiveBilling(loader BillingStatusLoader) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromContext(c.Request.Context())
		if !ok {
			apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, "authentication required", nil)
			return
		}
		if p.IsPlatformUser() {
			c.Next()
			return
		}

		status, err := loader.BillingStatus(c.Request.Context(), *p.CompanyID)
		if err != nil {
			_ = c.Error(err)
			apierror.Respond(c, http.StatusInternalServerError, apierror.CodeInternal, "internal server error", nil)
			return
		}
		c.Header(BillingStatusHeader, status)

		if !model.BillingAllowsUse(status) {
			apierror.Respond(c, http.StatusPaymentRequired, apierror.CodeBillingInactive,
				billingMessage(status), map[string]string{"billing_status": status})
			return
		}
		c.Next()
	}
}

// billingMessage says what to do, in words a company admin understands.
func billingMessage(status string) string {
	switch status {
	case model.BillingPending:
		return "your subscription starts once the first invoice is paid"
	case model.BillingBlocked:
		return "your subscription is suspended because payments failed; pay the open invoice to continue"
	default:
		return "your company has no active subscription; subscribe to continue"
	}
}
