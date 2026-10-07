package service

import (
	"context"
	"time"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// BillingMetricsStore is what BillingMetricsService needs
// (repository.BillingMetricsRepository).
type BillingMetricsStore interface {
	MRRByProduct(ctx context.Context) ([]model.ProductMRR, error)
	SubscriptionsByStatus(ctx context.Context) (map[string]int, error)
	CompaniesByBillingStatus(ctx context.Context) (map[string]int, error)
	Outstanding(ctx context.Context) (invoices int, amount int64, err error)
	PaymentFailuresSince(ctx context.Context, since time.Time) (int, error)
	Monthly(ctx context.Context, since time.Time) (map[string]model.MonthMetrics, error)
}

const (
	metricsDefaultMonths = 12
	metricsMaxMonths     = 36
)

// BillingMetricsService is the platform's billing dashboard: revenue across
// all companies, so platform only (also checked here, not just by the
// permission).
type BillingMetricsService struct {
	store BillingMetricsStore
	now   func() time.Time
}

func NewBillingMetricsService(store BillingMetricsStore) *BillingMetricsService {
	return &BillingMetricsService{store: store, now: time.Now}
}

// Get returns the dashboard with the last `months` calendar months (0 =
// the default 12), the current month included.
func (s *BillingMetricsService) Get(ctx context.Context, p auth.Principal, months int) (*model.BillingMetrics, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	if months == 0 {
		months = metricsDefaultMonths
	}
	if months < 1 || months > metricsMaxMonths {
		return nil, &validation.ValidationError{Fields: map[string]string{"months": "must be between 1 and 36"}}
	}

	now := s.now().UTC()
	firstMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(months - 1), 0)

	out := &model.BillingMetrics{}
	var err error
	if out.MRRByProduct, err = s.store.MRRByProduct(ctx); err != nil {
		return nil, err
	}
	for _, m := range out.MRRByProduct {
		out.MRR += m.MRR
	}
	if out.SubscriptionsByStatus, err = s.store.SubscriptionsByStatus(ctx); err != nil {
		return nil, err
	}
	if out.CompaniesByBillingStatus, err = s.store.CompaniesByBillingStatus(ctx); err != nil {
		return nil, err
	}
	if out.OutstandingInvoices, out.OutstandingAmount, err = s.store.Outstanding(ctx); err != nil {
		return nil, err
	}
	if out.PaymentFailures30d, err = s.store.PaymentFailuresSince(ctx, now.AddDate(0, 0, -30)); err != nil {
		return nil, err
	}
	byMonth, err := s.store.Monthly(ctx, firstMonth)
	if err != nil {
		return nil, err
	}

	// Every month present, oldest first: a chart needs the zeros too.
	out.Months = make([]model.MonthMetrics, 0, months)
	for m := firstMonth; !m.After(now); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		mm := byMonth[key]
		mm.Month = key
		out.Months = append(out.Months, mm)
	}
	return out, nil
}
