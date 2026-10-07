package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBillingStatusFor(t *testing.T) {
	assert.Equal(t, BillingNone, BillingStatusFor(nil), "no subscription")

	cases := map[string]struct {
		billing string
		allowed bool
	}{
		SubscriptionIncomplete:        {BillingPending, false}, // pay first, then use
		SubscriptionActive:            {BillingActive, true},
		SubscriptionTrialing:          {BillingActive, true},
		SubscriptionPastDue:           {BillingPastDue, true}, // grace period
		SubscriptionUnpaid:            {BillingBlocked, false},
		SubscriptionPaused:            {BillingBlocked, false},
		SubscriptionCanceled:          {BillingNone, false},
		SubscriptionIncompleteExpired: {BillingNone, false},
	}
	for status, want := range cases {
		got := BillingStatusFor(&Subscription{Status: status})
		assert.Equal(t, want.billing, got, status)
		assert.Equal(t, want.allowed, BillingAllowsUse(got), status)
	}
}
