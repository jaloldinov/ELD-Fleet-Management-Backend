package stripegw

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const invoiceJSON = `{"id":"in_1","object":"invoice","customer":"cus_1","number":"ABCD-0001","status":"open",
	"billing_reason":"subscription_cycle","currency":"usd","total":15000,"amount_due":15000,"amount_paid":0,
	"amount_remaining":15000,"hosted_invoice_url":"https://invoice.stripe.com/i/x","invoice_pdf":"https://pay.stripe.com/x.pdf",
	"created":1791331200,"period_start":1788652800,"period_end":1791331200,
	"status_transitions":{"finalized_at":1791334800,"paid_at":null},
	"parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_1"}},
	"confirmation_secret":{"client_secret":"pi_1_secret","type":"payment_intent"}}`

func TestInvoices_Get(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"GET /v1/invoices/in_1": {200, invoiceJSON},
		"GET /v1/invoices/in_1/lines": {200, `{"object":"list","has_more":false,"data":[
			{"object":"line_item","description":"12 × ELD Fleet plan","amount":30000,"quantity":12}]}`},
	})

	got, err := NewInvoices(c).GetInvoice(context.Background(), "in_1", true)

	require.NoError(t, err)
	assert.Equal(t, "cus_1", got.CustomerID)
	assert.Equal(t, "sub_1", got.StripeSubscriptionID)
	assert.Equal(t, "ABCD-0001", got.Number)
	assert.Equal(t, int64(15000), got.AmountRemaining)
	assert.Equal(t, "pi_1_secret", got.ClientSecret)
	require.NotNil(t, got.FinalizedAt)
	assert.Nil(t, got.PaidAt)
	assert.Equal(t, int64(1791331200), got.StripeCreatedAt.Unix())
	assert.Equal(t, []model.InvoiceLine{{Description: "12 × ELD Fleet plan", Amount: 30000, Quantity: 12}}, got.Lines)
	assert.Equal(t, "confirmation_secret", f.find(t, "GET /v1/invoices/in_1").Form.Get("expand[0]"))
}

func TestInvoices_Pay(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{"POST /v1/invoices/in_1/pay": {200, invoiceJSON}})

	_, err := NewInvoices(c).PayInvoice(context.Background(), "in_1")

	require.NoError(t, err)
	assert.Equal(t, "true", f.find(t, "POST /v1/invoices/in_1/pay").Form.Get("off_session"),
		"charged by the server, the card holder is not there")
}

func TestInvoices_Pay_ErrorsForTheUser(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"3-D Secure needed": {`{"error":{"type":"card_error","code":"authentication_required","message":"needs auth"}}`, model.ErrAuthenticationRequired},
		"declined":          {`{"error":{"type":"card_error","code":"card_declined","message":"Your card was declined."}}`, model.ErrCardDeclined},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, c := newRoutedFake(t, map[string]reply{"POST /v1/invoices/in_1/pay": {402, tc.body}})
			_, err := NewInvoices(c).PayInvoice(context.Background(), "in_1")
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestInvoices_OneOffCharge(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/invoices":               {200, `{"id":"in_9","object":"invoice","status":"draft"}`},
		"POST /v1/invoiceitems":           {200, `{"id":"ii_9","object":"invoiceitem"}`},
		"POST /v1/invoices/in_9/finalize": {200, `{"id":"in_9","object":"invoice","status":"open","total":15000,"amount_due":15000}`},
	})
	in := model.StripeOneOffCharge{ID: uuid.New(), CompanyID: uuid.New(), CustomerID: "cus_1", Description: "ELD installation", Amount: 15000}

	got, err := NewInvoices(c).CreateOneOffCharge(context.Background(), in)

	require.NoError(t, err)
	assert.Equal(t, model.InvoiceOpen, got.Status)
	inv := f.find(t, "POST /v1/invoices")
	assert.Equal(t, "exclude", inv.Form.Get("pending_invoice_items_behavior"),
		"prorations waiting for the renewal are not swept into this bill")
	assert.Equal(t, "charge_automatically", inv.Form.Get("collection_method"))
	assert.Equal(t, "charge-invoice-"+in.ID.String(), inv.IdempotencyKey)
	item := f.find(t, "POST /v1/invoiceitems")
	assert.Equal(t, "in_9", item.Form.Get("invoice"), "the line goes on THIS invoice")
	assert.Equal(t, "15000", item.Form.Get("amount"))
	f.find(t, "POST /v1/invoices/in_9/finalize")
}

func TestInvoices_BalanceAndCredit(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"GET /v1/customers/cus_1": {200, `{"id":"cus_1","object":"customer","balance":-5000}`},
		"GET /v1/customers/cus_1/balance_transactions": {200, `{"object":"list","has_more":false,"data":[
			{"id":"cbtxn_1","object":"customer_balance_transaction","type":"adjustment","amount":-5000,"ending_balance":-5000,
			 "description":"Goodwill","created":1791331200}]}`},
		"POST /v1/customers/cus_1/balance_transactions": {200, `{"id":"cbtxn_2","object":"customer_balance_transaction",
			"type":"adjustment","amount":-2500,"ending_balance":-7500,"created":1791331300}`},
	})
	g := NewInvoices(c)

	bal, err := g.Balance(context.Background(), "cus_1")
	require.NoError(t, err)
	assert.Equal(t, int64(-5000), bal.Balance, "negative = credit")
	require.Len(t, bal.Transactions, 1)
	assert.Equal(t, "Goodwill", bal.Transactions[0].Description)

	tx, err := g.AddCredit(context.Background(), "cus_1", 2500, "Outage in October", "credit-x")
	require.NoError(t, err)
	assert.Equal(t, int64(-7500), tx.EndingBalance)
	r := f.find(t, "POST /v1/customers/cus_1/balance_transactions")
	assert.Equal(t, "-2500", r.Form.Get("amount"), "credit is a NEGATIVE balance")
	assert.Equal(t, "credit-x", r.IdempotencyKey)
}

func TestInvoices_CreditNote(t *testing.T) {
	cases := map[string]struct {
		paid, refund bool
		field        string // where the amount goes
	}{
		"paid, refund to card":     {true, true, "refund_amount"},
		"paid, credit to balance":  {true, false, "credit_amount"},
		"open, lowers what is due": {false, false, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, c := newRoutedFake(t, map[string]reply{"POST /v1/credit_notes": {200,
				`{"id":"cn_1","object":"credit_note","number":"ABCD-0001-CN-01","amount":5000,"reason":"order_change",
				  "refunds":[{"amount_refunded":5000}],"created":1791331200}`}})

			_, err := NewInvoices(c).CreateCreditNote(context.Background(), model.StripeCreditNoteRequest{
				ID: uuid.New(), InvoiceID: "in_1", Amount: 5000, Paid: tc.paid, Refund: tc.refund, Reason: "order_change",
			})

			require.NoError(t, err)
			form := f.find(t, "POST /v1/credit_notes").Form
			assert.Equal(t, "5000", form.Get("amount"))
			for _, fld := range []string{"refund_amount", "credit_amount"} {
				if fld == tc.field {
					assert.Equal(t, "5000", form.Get(fld))
				} else {
					_, sent := form[fld]
					assert.False(t, sent, fld)
				}
			}
		})
	}
}
