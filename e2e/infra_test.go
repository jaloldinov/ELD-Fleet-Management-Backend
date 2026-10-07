package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Infrastructure and cross-cutting behaviour: what every request gets,
// whichever handler serves it.

func TestHealthAndReadiness(t *testing.T) {
	var live struct {
		Status string `json:"status"`
	}
	anon.do(t, http.MethodGet, "/health", nil, http.StatusOK, &live)
	assert.Equal(t, "ok", live.Status)

	var ready struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	anon.do(t, http.MethodGet, "/ready", nil, http.StatusOK, &ready)
	assert.Equal(t, "ok", ready.Status)
	assert.Equal(t, "up", ready.Database)
}

func TestSecurityHeaders(t *testing.T) {
	// Successful and failed responses alike; API and infrastructure routes.
	for _, path := range []string{"/health", "/ready", "/api/v1/auth/me"} {
		t.Run(path, func(t *testing.T) {
			h := anon.send(t, http.MethodGet, path, nil).Header
			assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
			assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
			assert.Equal(t, "no-store", h.Get("Cache-Control"))
			assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
			assert.Contains(t, h.Get("Content-Security-Policy"), "default-src 'none'")
			// Development (plain http): HSTS would lock browsers out of localhost.
			assert.Empty(t, h.Get("Strict-Transport-Security"))
		})
	}
}

func TestRequestID(t *testing.T) {
	generated := anon.send(t, http.MethodGet, "/health", nil).Header.Get("X-Request-ID")
	assert.Len(t, generated, 36, "a UUID is generated when the caller sends none")

	given := client{header: map[string]string{"X-Request-ID": "e2e-trace-42"}}
	assert.Equal(t, "e2e-trace-42", given.send(t, http.MethodGet, "/health", nil).Header.Get("X-Request-ID"),
		"a safe incoming ID is kept, so one ID follows a request across services")

	unsafe := client{header: map[string]string{"X-Request-ID": "bad id\twith tab"}}
	assert.NotEqual(t, "bad id\twith tab", unsafe.send(t, http.MethodGet, "/health", nil).Header.Get("X-Request-ID"),
		"unsafe IDs are replaced (they end up in logs)")
}

func TestUnknownRouteIsJSON(t *testing.T) {
	e := anon.fail(t, http.MethodGet, "/api/v1/no-such-thing", nil, http.StatusNotFound)
	assert.Equal(t, "not_found", e.Code)
}

func TestSwaggerServedInDevelopment(t *testing.T) {
	anon.do(t, http.MethodGet, "/swagger/index.html", nil, http.StatusOK, nil)
	spec := anon.do(t, http.MethodGet, "/swagger/doc.json", nil, http.StatusOK, nil)
	assert.Contains(t, string(spec.Body), "ELD Fleet Management API")
}

func TestStrictJSONBodies(t *testing.T) {
	p := platform(t)
	const path = "/api/v1/companies"

	cases := []struct {
		name     string
		body     string
		code     string
		detailOn string // field expected in details, if any
	}{
		{"broken JSON", `{"name":`, "bad_request", ""},
		{"empty body", ``, "bad_request", ""},
		{"two JSON values", `{}{}`, "bad_request", ""},
		{"unknown field (typo)", `{"nmae":"Acme","dot_number":"1","timezone":"UTC"}`, "validation_failed", "nmae"},
		{"wrong type", `{"name":5,"dot_number":"1","timezone":"UTC"}`, "validation_failed", "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := p.fail(t, http.MethodPost, path, tc.body, http.StatusBadRequest)
			assert.Equal(t, tc.code, e.Code)
			if tc.detailOn != "" {
				assert.Contains(t, e.Details, tc.detailOn)
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	// 2 MB: over the 1 MB limit. On a public route, so the limit protects
	// even endpoints that need no token.
	huge := `{"login":"` + strings.Repeat("a", 2<<20) + `","password":"x"}`
	e := anon.fail(t, http.MethodPost, "/api/v1/auth/login", huge, http.StatusRequestEntityTooLarge)
	assert.Equal(t, "payload_too_large", e.Code)
}

func TestInvalidPathAndQueryParams(t *testing.T) {
	p := platform(t)

	e := p.fail(t, http.MethodGet, "/api/v1/companies/not-a-uuid", nil, http.StatusBadRequest)
	assert.Equal(t, "validation_failed", e.Code)
	assert.Contains(t, e.Details, "id")

	e = p.fail(t, http.MethodGet, "/api/v1/companies?page=abc", nil, http.StatusBadRequest)
	assert.Contains(t, e.Details, "page")

	e = p.fail(t, http.MethodGet, "/api/v1/trucks?is_active=maybe", nil, http.StatusBadRequest)
	assert.Contains(t, e.Details, "is_active")

	e = p.fail(t, http.MethodGet, "/api/v1/audit-logs?from=yesterday", nil, http.StatusBadRequest)
	assert.Contains(t, e.Details, "from")
}

func TestPagination(t *testing.T) {
	p := platform(t)
	newTenant(t)
	newTenant(t)

	var all page
	p.do(t, http.MethodGet, "/api/v1/companies", nil, http.StatusOK, &all)
	require.GreaterOrEqual(t, all.Total, 2)
	assert.Equal(t, 1, all.Page)
	assert.Equal(t, 20, all.PageSize, "default page size")

	var first struct {
		page
		Items []companyResp `json:"items"`
	}
	p.do(t, http.MethodGet, "/api/v1/companies?page=1&page_size=1", nil, http.StatusOK, &first)
	require.Len(t, first.Items, 1)
	assert.Equal(t, all.Total, first.Total, "total counts every row, not just this page")

	var second struct {
		Items []companyResp `json:"items"`
	}
	p.do(t, http.MethodGet, "/api/v1/companies?page=2&page_size=1", nil, http.StatusOK, &second)
	require.Len(t, second.Items, 1)
	assert.NotEqual(t, first.Items[0].ID, second.Items[0].ID, "pages do not overlap")
}

func TestFrontendServed(t *testing.T) {
	// The http client follows the redirect from / to /app/.
	r := anon.do(t, http.MethodGet, "/", nil, http.StatusOK, nil)
	assert.Contains(t, string(r.Body), "<title>ELD Billing</title>")

	csp := r.Header.Get("Content-Security-Policy")
	assert.Contains(t, csp, "https://js.stripe.com")
	assert.NotContains(t, csp, "unsafe-inline")

	js := anon.do(t, http.MethodGet, "/app/app.js", nil, http.StatusOK, nil)
	assert.Contains(t, js.Header.Get("Content-Type"), "javascript")
}

func TestBillingRoutesAbsentWithoutStripe(t *testing.T) {
	// The e2e server runs without Stripe keys: billing does not exist.
	p := platform(t)
	e := p.fail(t, http.MethodGet, "/api/v1/products", nil, http.StatusNotFound)
	assert.Equal(t, "route not found", e.Message)
}
