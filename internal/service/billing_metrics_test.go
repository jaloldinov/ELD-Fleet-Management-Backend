package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeMetricsStore struct {
	since time.Time
	fails time.Time
}

func (f *fakeMetricsStore) MRRByProduct(context.Context) ([]model.ProductMRR, error) {
	return []model.ProductMRR{{ProductName: "ELD", MRR: 15000}, {ProductName: "Support", MRR: 10000}}, nil
}
func (f *fakeMetricsStore) SubscriptionsByStatus(context.Context) (map[string]int, error) {
	return map[string]int{"active": 2}, nil
}
func (f *fakeMetricsStore) CompaniesByBillingStatus(context.Context) (map[string]int, error) {
	return map[string]int{"active": 2, "blocked": 1}, nil
}
func (f *fakeMetricsStore) Outstanding(context.Context) (int, int64, error) { return 1, 4200, nil }
func (f *fakeMetricsStore) PaymentFailuresSince(_ context.Context, since time.Time) (int, error) {
	f.fails = since
	return 3, nil
}
func (f *fakeMetricsStore) Monthly(_ context.Context, since time.Time) (map[string]model.MonthMetrics, error) {
	f.since = since
	return map[string]model.MonthMetrics{
		"2026-08": {Month: "2026-08", Revenue: 7500},
		"2026-10": {Month: "2026-10", Revenue: 15000, NewSubscriptions: 4, CanceledSubscriptions: 1},
	}, nil
}

func newMetricsSvc() (*BillingMetricsService, *fakeMetricsStore) {
	store := &fakeMetricsStore{}
	svc := NewBillingMetricsService(store)
	svc.now = func() time.Time { return time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC) }
	return svc, store
}

func TestBillingMetrics(t *testing.T) {
	svc, store := newMetricsSvc()

	got, err := svc.Get(context.Background(), platformAdmin, 4)

	require.NoError(t, err)
	assert.Equal(t, int64(25000), got.MRR, "the sum of the products")
	assert.Equal(t, 1, got.OutstandingInvoices)
	assert.Equal(t, 3, got.PaymentFailures30d)
	assert.Equal(t, time.Date(2026, 9, 7, 15, 0, 0, 0, time.UTC), store.fails, "last 30 days")
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), store.since, "4 months: Jul, Aug, Sep, Oct")

	months := make([]string, 0, len(got.Months))
	for _, m := range got.Months {
		months = append(months, m.Month)
	}
	assert.Equal(t, []string{"2026-07", "2026-08", "2026-09", "2026-10"}, months, "every month, oldest first")
	assert.Equal(t, int64(0), got.Months[0].Revenue, "an empty month is zero, not missing")
	assert.Equal(t, int64(7500), got.Months[1].Revenue)
	assert.Equal(t, 4, got.Months[3].NewSubscriptions)
}

func TestBillingMetrics_Rules(t *testing.T) {
	svc, _ := newMetricsSvc()
	ctx := context.Background()

	_, err := svc.Get(ctx, companyAUser, 12)
	assert.ErrorIs(t, err, model.ErrForbidden, "revenue of all companies: platform only")

	for _, bad := range []int{-1, 37} {
		_, err = svc.Get(ctx, platformAdmin, bad)
		requireValidationField(t, err, "months")
	}

	got, err := svc.Get(ctx, platformAdmin, 0)
	require.NoError(t, err)
	assert.Len(t, got.Months, 12, "the default")
}
