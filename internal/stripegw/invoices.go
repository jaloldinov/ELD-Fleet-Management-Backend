package stripegw

import (
	"context"
	"errors"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Invoices reads and acts on Stripe invoices, one-off charges, credits and
// credit notes.
type Invoices struct {
	c *stripe.Client
}

func NewInvoices(c *stripe.Client) *Invoices {
	return &Invoices{c: c}
}

// GetInvoice reads an invoice; withLines also reads all its lines.
func (g *Invoices) GetInvoice(ctx context.Context, id string, withLines bool) (*model.StripeInvoiceSnapshot, error) {
	params := &stripe.InvoiceRetrieveParams{}
	params.AddExpand(expandInvoiceSecret)
	inv, err := g.c.V1Invoices.Retrieve(ctx, id, params)
	if err != nil {
		if isResourceMissing(err) {
			return nil, model.ErrNotFound
		}
		return nil, wrap("retrieve invoice", err)
	}
	snap := toInvoiceSnapshot(inv)
	if withLines {
		snap.Lines = []model.InvoiceLine{}
		for l, err := range g.c.V1Invoices.ListLines(ctx, &stripe.InvoiceListLinesParams{Invoice: stripe.String(id)}).All(ctx) {
			if err != nil {
				return nil, wrap("list invoice lines", err)
			}
			snap.Lines = append(snap.Lines, model.InvoiceLine{
				Description: l.Description, Amount: l.Amount, Quantity: int(l.Quantity), Proration: isProration(l),
			})
		}
	}
	return snap, nil
}

// PayInvoice charges an open invoice now, with the customer's default card
// and without the card holder present (off_session). If the bank insists
// on 3-D Secure: model.ErrAuthenticationRequired (finish in the browser).
func (g *Invoices) PayInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	params := &stripe.InvoicePayParams{OffSession: stripe.Bool(true)}
	params.AddExpand(expandInvoiceSecret)
	inv, err := g.c.V1Invoices.Pay(ctx, id, params)
	if err != nil {
		var sErr *stripe.Error
		if errors.As(err, &sErr) && sErr.Code == stripe.ErrorCodeAuthenticationRequired {
			return nil, model.ErrAuthenticationRequired
		}
		return nil, wrap("pay invoice", err)
	}
	return toInvoiceSnapshot(inv), nil
}

// FinalizeInvoice turns a draft into a real bill (open): it gets a number,
// cannot change any more, and is collected.
func (g *Invoices) FinalizeInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	params := &stripe.InvoiceFinalizeInvoiceParams{}
	params.AddExpand(expandInvoiceSecret)
	inv, err := g.c.V1Invoices.FinalizeInvoice(ctx, id, params)
	if err != nil {
		return nil, wrap("finalize invoice", err)
	}
	return toInvoiceSnapshot(inv), nil
}

// VoidInvoice cancels an open invoice: it is kept for the record but
// nothing is owed any more.
func (g *Invoices) VoidInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	inv, err := g.c.V1Invoices.VoidInvoice(ctx, id, nil)
	if err != nil {
		return nil, wrap("void invoice", err)
	}
	return toInvoiceSnapshot(inv), nil
}

// CreateOneOffCharge creates and finalizes a separate invoice with one
// line. pending_invoice_items_behavior=exclude: prorations waiting for the
// next renewal (e.g. trucks added this month) stay where they are instead
// of being swept into this bill.
func (g *Invoices) CreateOneOffCharge(ctx context.Context, in model.StripeOneOffCharge) (*model.StripeInvoiceSnapshot, error) {
	invParams := &stripe.InvoiceCreateParams{
		Customer:                    stripe.String(in.CustomerID),
		CollectionMethod:            stripe.String("charge_automatically"),
		PendingInvoiceItemsBehavior: stripe.String("exclude"),
		AutoAdvance:                 stripe.Bool(true),
		Description:                 stripe.String(in.Description),
		Metadata:                    map[string]string{MetaCompanyID: in.CompanyID.String(), "eld_charge_id": in.ID.String()},
	}
	invParams.SetIdempotencyKey("charge-invoice-" + in.ID.String())
	inv, err := g.c.V1Invoices.Create(ctx, invParams)
	if err != nil {
		return nil, wrap("create invoice", err)
	}

	itemParams := &stripe.InvoiceItemCreateParams{
		Customer:    stripe.String(in.CustomerID),
		Invoice:     stripe.String(inv.ID),
		Amount:      stripe.Int64(in.Amount),
		Currency:    stripe.String(model.CurrencyUSD),
		Description: stripe.String(in.Description),
	}
	itemParams.SetIdempotencyKey("charge-item-" + in.ID.String())
	if _, err := g.c.V1InvoiceItems.Create(ctx, itemParams); err != nil {
		return nil, wrap("create invoice item", err)
	}
	return g.FinalizeInvoice(ctx, inv.ID)
}

// Balance returns the customer's credit balance and its latest changes.
func (g *Invoices) Balance(ctx context.Context, customerID string) (*model.CustomerBalance, error) {
	cust, err := g.c.V1Customers.Retrieve(ctx, customerID, nil)
	if err != nil {
		return nil, wrap("retrieve customer", err)
	}
	out := &model.CustomerBalance{Balance: cust.Balance, Transactions: []model.BalanceTransaction{}}
	params := &stripe.CustomerBalanceTransactionListParams{Customer: stripe.String(customerID)}
	params.Limit = stripe.Int64(25)
	list := g.c.V1CustomerBalanceTransactions.List(ctx, params)
	for _, t := range list.Data() {
		bt := model.BalanceTransaction{
			ID: t.ID, Type: string(t.Type), Amount: t.Amount, EndingBalance: t.EndingBalance,
			Description: t.Description, CreatedAt: *unixTime(t.Created),
		}
		if t.Invoice != nil {
			bt.InvoiceID = t.Invoice.ID
		}
		out.Transactions = append(out.Transactions, bt)
	}
	if err := list.Err(); err != nil {
		return nil, wrap("list balance transactions", err)
	}
	return out, nil
}

// AddCredit gives the customer credit: a negative balance transaction.
// Stripe uses it automatically to pay the next invoices.
func (g *Invoices) AddCredit(ctx context.Context, customerID string, amount int64, description, idempotencyKey string) (*model.BalanceTransaction, error) {
	params := &stripe.CustomerBalanceTransactionCreateParams{
		Customer:    stripe.String(customerID),
		Amount:      stripe.Int64(-amount),
		Currency:    stripe.String(model.CurrencyUSD),
		Description: stripe.String(description),
	}
	params.SetIdempotencyKey(idempotencyKey)
	t, err := g.c.V1CustomerBalanceTransactions.Create(ctx, params)
	if err != nil {
		return nil, wrap("add credit", err)
	}
	return &model.BalanceTransaction{ID: t.ID, Type: string(t.Type), Amount: t.Amount, EndingBalance: t.EndingBalance,
		Description: t.Description, CreatedAt: *unixTime(t.Created)}, nil
}

// CreateCreditNote reduces a finalized invoice. On a paid invoice the
// amount must go somewhere: refund_amount (back to the card) or
// credit_amount (to the balance).
func (g *Invoices) CreateCreditNote(ctx context.Context, in model.StripeCreditNoteRequest) (*model.CreditNote, error) {
	params := &stripe.CreditNoteCreateParams{
		Invoice: stripe.String(in.InvoiceID),
		Amount:  stripe.Int64(in.Amount),
	}
	if in.Paid {
		if in.Refund {
			params.RefundAmount = stripe.Int64(in.Amount)
		} else {
			params.CreditAmount = stripe.Int64(in.Amount)
		}
	}
	if in.Reason != "" {
		params.Reason = stripe.String(in.Reason)
	}
	if in.Memo != "" {
		params.Memo = stripe.String(in.Memo)
	}
	params.SetIdempotencyKey("credit-note-" + in.ID.String())
	cn, err := g.c.V1CreditNotes.Create(ctx, params)
	if err != nil {
		return nil, wrap("create credit note", err)
	}
	return toCreditNote(cn), nil
}

// ListCreditNotes returns the credit notes of an invoice.
func (g *Invoices) ListCreditNotes(ctx context.Context, invoiceID string) ([]model.CreditNote, error) {
	out := []model.CreditNote{}
	for cn, err := range g.c.V1CreditNotes.List(ctx, &stripe.CreditNoteListParams{Invoice: stripe.String(invoiceID)}).All(ctx) {
		if err != nil {
			return nil, wrap("list credit notes", err)
		}
		out = append(out, *toCreditNote(cn))
	}
	return out, nil
}

// The payment secret of an invoice, for finishing 3-D Secure in the browser.
const expandInvoiceSecret = "confirmation_secret"

func toInvoiceSnapshot(inv *stripe.Invoice) *model.StripeInvoiceSnapshot {
	s := &model.StripeInvoiceSnapshot{Invoice: model.Invoice{
		StripeInvoiceID: inv.ID, Number: inv.Number, Status: string(inv.Status),
		BillingReason: string(inv.BillingReason), Description: inv.Description, Currency: string(inv.Currency),
		Total: inv.Total, AmountDue: inv.AmountDue, AmountPaid: inv.AmountPaid, AmountRemaining: inv.AmountRemaining,
		HostedInvoiceURL: inv.HostedInvoiceURL, InvoicePDF: inv.InvoicePDF,
		PeriodStart: unixTime(inv.PeriodStart), PeriodEnd: unixTime(inv.PeriodEnd),
	}}
	if t := unixTime(inv.Created); t != nil {
		s.StripeCreatedAt = *t
	}
	if inv.Customer != nil {
		s.CustomerID = inv.Customer.ID
	}
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil && inv.Parent.SubscriptionDetails.Subscription != nil {
		s.StripeSubscriptionID = inv.Parent.SubscriptionDetails.Subscription.ID
	}
	if st := inv.StatusTransitions; st != nil {
		s.FinalizedAt, s.PaidAt, s.VoidedAt = unixTime(st.FinalizedAt), unixTime(st.PaidAt), unixTime(st.VoidedAt)
	}
	if inv.ConfirmationSecret != nil {
		s.ClientSecret = inv.ConfirmationSecret.ClientSecret
	}
	return s
}

// toCreditNote: where the money went is not one field in Stripe. Refunds
// are a list; credit to the balance shows as a balance transaction.
func toCreditNote(cn *stripe.CreditNote) *model.CreditNote {
	out := &model.CreditNote{
		ID: cn.ID, Number: cn.Number, Amount: cn.Amount, Reason: string(cn.Reason), Memo: cn.Memo,
	}
	for _, r := range cn.Refunds {
		out.RefundAmount += r.AmountRefunded
	}
	if cn.CustomerBalanceTransaction != nil {
		out.CreditAmount = cn.Amount - out.RefundAmount - cn.OutOfBandAmount
	}
	if t := unixTime(cn.Created); t != nil {
		out.CreatedAt = *t
	}
	return out
}
