package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/pkg/vin"
)

// client calls the API under test, optionally as a logged-in user.
//
// Responses are decoded into the tests' own small structs (not the handler
// DTOs): the JSON on the wire is the contract, so a renamed field must fail
// here even if the Go code still compiles.
type client struct {
	token   string            // access token; empty = anonymous
	refresh string            // refresh token of the same session
	header  map[string]string // extra request headers
	base    string            // server URL; "" = baseURL (billing off)
}

// on returns the same session talking to another server (both servers
// share the database and the JWT secret, so a token works on both).
func (c client) on(base string) client {
	c.base = base
	return c
}

var anon = client{}

// response is a finished request: status, headers and the raw body.
type response struct {
	method, path string
	Status       int
	Header       http.Header
	Body         []byte
}

// send does the request. body may be nil, a []byte/string (sent as is) or
// anything else (sent as JSON).
func (c client) send(t *testing.T, method, path string, body any) *response {
	t.Helper()

	var reqBody io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reqBody = bytes.NewReader(b)
	case string:
		reqBody = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		require.NoError(t, err)
		reqBody = bytes.NewReader(j)
	}
	base := c.base
	if base == "" {
		base = baseURL
	}
	req, err := http.NewRequest(method, base+path, reqBody)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return &response{method: method, path: path, Status: resp.StatusCode, Header: resp.Header, Body: respBody}
}

// do sends the request, requires the wanted status and decodes the body into
// out (when not nil). On a wrong status the body is shown: it usually
// explains the failure.
func (c client) do(t *testing.T, method, path string, body any, wantStatus int, out any) *response {
	t.Helper()
	r := c.send(t, method, path, body)
	r.require(t, wantStatus)
	if out != nil {
		r.decode(t, out)
	}
	return r
}

func (r *response) require(t *testing.T, status int) {
	t.Helper()
	require.Equal(t, status, r.Status, "%s %s: body: %s", r.method, r.path, r.Body)
}

func (r *response) decode(t *testing.T, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.Body, out), "%s %s: body: %s", r.method, r.path, r.Body)
}

// apiError is the API's error body.
type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details"`
}

// fail sends the request, requires the wanted (error) status and returns
// the decoded error body.
func (c client) fail(t *testing.T, method, path string, body any, wantStatus int) apiError {
	t.Helper()
	var e struct {
		Error apiError `json:"error"`
	}
	c.do(t, method, path, body, wantStatus, &e)
	require.NotEmpty(t, e.Error.Code, "error responses always carry a code")
	return e.Error
}

// ---------- Response shapes ----------

type tokenPair struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
}

type page struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

type companyResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DOTNumber string `json:"dot_number"`
	Timezone  string `json:"timezone"`
	HOSCycle  string `json:"hos_cycle"`
	IsActive  bool   `json:"is_active"`
}

type userResp struct {
	ID          string   `json:"id"`
	CompanyID   *string  `json:"company_id"`
	Email       *string  `json:"email"`
	Username    *string  `json:"username"`
	FirstName   string   `json:"first_name"`
	LastName    string   `json:"last_name"`
	IsActive    bool     `json:"is_active"`
	Roles       []string `json:"roles"`
	LastLoginAt *string  `json:"last_login_at"`
	Permissions []string `json:"permissions"` // only on /auth/me
}

type driverResp struct {
	ID               string  `json:"id"`
	UserID           string  `json:"user_id"`
	CompanyID        string  `json:"company_id"`
	Email            *string `json:"email"`
	Username         *string `json:"username"`
	FirstName        string  `json:"first_name"`
	IsActive         bool    `json:"is_active"`
	LicenseNumber    string  `json:"license_number"`
	LicenseState     string  `json:"license_state"`
	LicenseExpiresAt string  `json:"license_expires_at"`
}

type truckResp struct {
	ID           string  `json:"id"`
	CompanyID    string  `json:"company_id"`
	UnitNumber   string  `json:"unit_number"`
	VIN          string  `json:"vin"`
	Make         string  `json:"make"`
	Year         *int    `json:"year"`
	LicensePlate *string `json:"license_plate"`
	PlateState   *string `json:"plate_state"`
	IsActive     bool    `json:"is_active"`
}

type eldResp struct {
	ID              string  `json:"id"`
	CompanyID       string  `json:"company_id"`
	SerialNumber    string  `json:"serial_number"`
	Model           string  `json:"model"`
	FirmwareVersion string  `json:"firmware_version"`
	TruckID         *string `json:"truck_id"`
	IsActive        bool    `json:"is_active"`
}

// ---------- Unique test data ----------

var seq atomic.Int64

// next returns a number unique within this run; tests use it for DOT
// numbers, logins, VINs and serials, so they never collide with each other.
func next() int64 { return seq.Add(1) }

// uniq returns prefix + a unique number, e.g. "user17".
func uniq(prefix string) string { return fmt.Sprintf("%s%d", prefix, next()) }

// newVIN returns a valid, unique VIN: a fixed prefix, a unique serial, and
// the check digit (position 9) computed per 49 CFR 565.
func newVIN(t *testing.T) string {
	t.Helper()
	v := []byte(fmt.Sprintf("1M8GDM9A0KP%06d", next()))
	translit := map[byte]int{
		'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8,
		'J': 1, 'K': 2, 'L': 3, 'M': 4, 'N': 5, 'P': 7, 'R': 9,
		'S': 2, 'T': 3, 'U': 4, 'V': 5, 'W': 6, 'X': 7, 'Y': 8, 'Z': 9,
	}
	weights := []int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}
	sum := 0
	for i, ch := range v {
		n, ok := translit[ch]
		if !ok {
			n = int(ch - '0')
		}
		sum += n * weights[i]
	}
	if r := sum % 11; r == 10 {
		v[8] = 'X'
	} else {
		v[8] = byte('0' + r)
	}
	require.True(t, vin.Valid(string(v)), "test bug: generated an invalid VIN %s", v)
	return string(v)
}

// ---------- Fixtures ----------

// login signs in and returns a client for that session.
func login(t *testing.T, loginName, password string) client {
	t.Helper()
	var p tokenPair
	anon.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"login": loginName, "password": password}, http.StatusOK, &p)
	require.NotEmpty(t, p.AccessToken)
	require.NotEmpty(t, p.RefreshToken)
	return client{token: p.AccessToken, refresh: p.RefreshToken}
}

var (
	platformOnce   sync.Once
	platformClient client
)

// platform is the platform admin created by cmd/createadmin in TestMain.
// One login for the whole run (the access token lives 15 minutes).
func platform(t *testing.T) client {
	t.Helper()
	platformOnce.Do(func() { platformClient = login(t, adminEmail, adminPassword) })
	require.NotEmpty(t, platformClient.token, "platform admin login failed earlier")
	return platformClient
}

// tenant is a company with its admin, created through the API.
type tenant struct {
	ID         string
	AdminID    string
	AdminEmail string
	Admin      client
}

const userPassword = "e2e user password"

// newTenant onboards a company the way the platform does: create the
// company, create its first admin, log the admin in.
func newTenant(t *testing.T) tenant {
	t.Helper()
	p := platform(t)

	var c companyResp
	p.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
		"name": uniq("Carrier "), "dot_number": fmt.Sprintf("%d", 2000000+next()), "timezone": "America/Chicago",
	}, http.StatusCreated, &c)

	adminEmail := uniq("admin") + "@e2e.test"
	var u userResp
	p.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"company_id": c.ID, "email": adminEmail, "password": userPassword,
		"first_name": "Ada", "last_name": "Admin", "roles": []string{"admin"},
	}, http.StatusCreated, &u)

	return tenant{ID: c.ID, AdminID: u.ID, AdminEmail: adminEmail, Admin: login(t, adminEmail, userPassword)}
}

// newUser creates a user with roles in the tenant and logs them in.
func (tn tenant) newUser(t *testing.T, roles ...string) (userResp, client) {
	t.Helper()
	email := uniq("user") + "@e2e.test"
	var u userResp
	tn.Admin.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"email": email, "password": userPassword, "first_name": "Uma", "last_name": "User", "roles": roles,
	}, http.StatusCreated, &u)
	return u, login(t, email, userPassword)
}

func (tn tenant) newTruck(t *testing.T) truckResp {
	t.Helper()
	var tr truckResp
	tn.Admin.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
		"unit_number": uniq("U"), "vin": newVIN(t),
	}, http.StatusCreated, &tr)
	return tr
}

func (tn tenant) newELD(t *testing.T) eldResp {
	t.Helper()
	var d eldResp
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices", map[string]any{
		"serial_number": uniq("SN-"),
	}, http.StatusCreated, &d)
	return d
}

// newDriver creates a driver who logs in with a username, and logs them in.
func (tn tenant) newDriver(t *testing.T) (driverResp, client) {
	t.Helper()
	username := uniq("driver.")
	var d driverResp
	tn.Admin.do(t, http.MethodPost, "/api/v1/drivers", map[string]any{
		"username": username, "password": userPassword, "first_name": "Bob", "last_name": "Lee",
		"license_number": uniq("D"), "license_state": "TX", "license_expires_at": "2030-05-31",
	}, http.StatusCreated, &d)
	return d, login(t, username, userPassword)
}
