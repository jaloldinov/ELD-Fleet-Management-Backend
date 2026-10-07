package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func TestSchedule_SwitchToYearlyAtRenewal(t *testing.T) {
	f := activeSubFixture(t) // monthly: perTruck × 3 + flat
	ctx := context.Background()

	sub, err := f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.yearly)})

	require.NoError(t, err)
	// Sent to Stripe: today's plan until the renewal, then yearly × 3 trucks.
	require.NotNil(t, f.gw.scheduled)
	assert.Equal(t, "", f.gw.scheduled.ScheduleID, "no schedule yet: create one")
	assert.Equal(t, []model.StripeSubscriptionItem{{StripePriceID: "price_truck", Quantity: 3}, {StripePriceID: "price_flat", Quantity: 1}},
		f.gw.scheduled.CurrentItems)
	assert.Equal(t, []model.StripeSubscriptionItem{{StripePriceID: "price_year", Quantity: 3}}, f.gw.scheduled.NextItems)
	assert.Equal(t, model.IntervalYear, f.gw.scheduled.NextInterval)

	// Nothing changes today; the scheduled plan is stored.
	assert.Len(t, sub.Items, 2)
	assert.Equal(t, model.IntervalMonth, sub.Interval)
	require.NotNil(t, sub.ScheduledChangeAt)
	require.Len(t, sub.ScheduledItems, 1)
	assert.Equal(t, int64(25000), sub.ScheduledItems[0].Price.UnitAmount)
	assert.NotContains(t, f.gw.calls, "ChangeItems sub_1", "no charge, no proration now")
	assert.Equal(t, []string{"Plan × 3"}, f.audit.last(t).Changes["scheduled_items"].New)

	// Scheduling again replaces the plan in the SAME schedule.
	_, err = f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
	require.NoError(t, err)
	assert.Equal(t, "sub_sched_1", f.gw.scheduled.ScheduleID)
}

func TestSchedule_Cancel(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()
	_, err := f.svc.CancelScheduledChange(ctx, companyAUser, companyA)
	requireValidationField(t, err, "subscription") // nothing scheduled yet

	_, err = f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
	require.NoError(t, err)

	sub, err := f.svc.CancelScheduledChange(ctx, companyAUser, companyA)
	require.NoError(t, err)
	assert.Contains(t, f.gw.calls, "ReleaseSchedule sub_sched_1")
	assert.Nil(t, sub.ScheduledChangeAt)
	assert.Empty(t, sub.ScheduledItems)
	assert.Len(t, sub.Items, 2, "renews with today's plan")
}

func TestSchedule_OtherChangesWaitForIt(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()
	_, err := f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
	require.NoError(t, err)

	_, err = f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.yearly)})
	requireValidationField(t, err, "subscription")
	_, err = f.svc.PreviewChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.yearly)})
	requireValidationField(t, err, "subscription")
	_, err = f.svc.Cancel(ctx, companyAUser, companyA, true)
	requireValidationField(t, err, "subscription")

	// The platform can still end it now (support decision).
	_, err = f.svc.Cancel(ctx, platformAdmin, companyA, false)
	assert.NoError(t, err)
}

func TestSchedule_Rules(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()

	_, err := f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck, f.flat)})
	requireValidationField(t, err, "price_ids") // the current plan

	_, err = f.svc.Cancel(ctx, companyAUser, companyA, true)
	require.NoError(t, err)
	_, err = f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
	requireValidationField(t, err, "subscription") // ends at the renewal anyway
}

func TestSchedule_IdleScheduleIsReleasedBeforeAChange(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()
	// The renewal happened: the schedule's last phase runs, nothing is left
	// to schedule, but the schedule is still attached.
	f.gw.current.Schedule = &model.StripeSchedule{ID: "sub_sched_old"}
	_, err := f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	f.gw.calls = nil

	_, err = f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})

	require.NoError(t, err)
	assert.Equal(t, []string{"ReleaseSchedule sub_sched_old", "ChangeItems sub_1"}, f.gw.calls)
}

func TestSchedule_TrucksFollowIntoTheScheduledPlan(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()
	_, err := f.svc.ScheduleChange(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.yearly)})
	require.NoError(t, err)
	f.gw.calls = nil

	f.svc.trucks = fakeTrucks(5)
	changed, err := f.svc.SyncTruckQuantity(ctx, companyA)

	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{"SetQuantities sub_1", "ScheduleChange sub_1 schedule=sub_sched_1"}, f.gw.calls)
	assert.Equal(t, []model.StripeSubscriptionItem{{StripePriceID: "price_year", Quantity: 5}}, f.gw.scheduled.NextItems,
		"otherwise the fleet would shrink back to 3 at the renewal")
	sub, err := f.svc.Current(ctx, companyAUser, companyA)
	require.NoError(t, err)
	assert.Equal(t, 5, sub.ScheduledItems[0].Quantity)
}
