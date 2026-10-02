// Package service is the business-logic layer.
//
// Rules for this layer:
//   - no HTTP knowledge (no gin, no status codes)
//   - no SQL knowledge (no pgx); data access goes through interfaces
//   - validates input and enforces business rules
//   - returns errors instead of logging them (the HTTP layer logs once)
package service

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

const (
	companyNameMaxLen = 200

	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Same rule as the companies_dot_number_format CHECK constraint in the database.
var dotNumberRe = regexp.MustCompile(`^[0-9]{1,8}$`)

// CompanyRepository is what CompanyService needs from the data layer.
// It is defined here, by the consumer: *repository.CompanyRepository satisfies
// it in production, and a fake satisfies it in tests.
type CompanyRepository interface {
	Create(ctx context.Context, c model.Company) (*model.Company, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
	List(ctx context.Context, f model.CompanyFilter, limit, offset int) ([]model.Company, int, error)
	Update(ctx context.Context, c model.Company) (*model.Company, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// CreateCompanyInput is the data needed to create a company.
type CreateCompanyInput struct {
	Name      string
	DOTNumber string
	Timezone  string
}

// UpdateCompanyInput is a partial update: nil fields are left unchanged.
type UpdateCompanyInput struct {
	Name      *string
	DOTNumber *string
	Timezone  *string
	IsActive  *bool
}

// ListParams asks for one page. Zero values mean "use the default".
type ListParams struct {
	Page     int // 1-based
	PageSize int
}

// CompanyPage is one page of results plus what a client needs to paginate.
type CompanyPage struct {
	Items    []model.Company
	Total    int
	Page     int
	PageSize int
}

type CompanyService struct {
	repo CompanyRepository
}

func NewCompanyService(repo CompanyRepository) *CompanyService {
	return &CompanyService{repo: repo}
}

// Every method takes the acting Principal explicitly: the compiler forces each
// caller to say WHO is acting, so tenant scoping cannot be forgotten.
// Permissions (what) are already checked by middleware; these methods add the
// tenant scope (where) and business rules.

// Create is for platform users only.
func (s *CompanyService) Create(ctx context.Context, p auth.Principal, in CreateCompanyInput) (*model.Company, error) {
	// Defense in depth: company:create is only seeded for platform_admin, but
	// even if a company role were granted it by mistake, a carrier must not
	// create other carriers.
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}

	c := model.Company{
		Name:      strings.TrimSpace(in.Name),
		DOTNumber: strings.TrimSpace(in.DOTNumber),
		Timezone:  strings.TrimSpace(in.Timezone),
	}

	// Validate BEFORE touching the database: cheap, and gives clear messages.
	if err := validateCompany(c); err != nil {
		return nil, err
	}

	return s.repo.Create(ctx, c)
}

func (s *CompanyService) GetByID(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Company, error) {
	if err := checkCompanyScope(p, id); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

// List returns all companies for platform users, and only their own company
// for company users.
func (s *CompanyService) List(ctx context.Context, p auth.Principal, params ListParams) (*CompanyPage, error) {
	page, size := normalizePage(params)

	var f model.CompanyFilter
	if !p.IsPlatformUser() {
		f.OnlyID = p.CompanyID // the scope is applied in SQL, not filtered in Go
	}

	items, total, err := s.repo.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}

	return &CompanyPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

// Update applies a partial update: read the current company, change only the
// fields that were sent, validate the result, then save.
func (s *CompanyService) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateCompanyInput) (*model.Company, error) {
	if err := checkCompanyScope(p, id); err != nil {
		return nil, err
	}
	// Field-level rule: suspending/reactivating a carrier is a platform
	// (billing) decision. A company admin may edit their company, not this.
	if in.IsActive != nil && !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}

	if in.Name == nil && in.DOTNumber == nil && in.Timezone == nil && in.IsActive == nil {
		return nil, &model.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err // includes model.ErrNotFound
	}

	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.DOTNumber != nil {
		c.DOTNumber = strings.TrimSpace(*in.DOTNumber)
	}
	if in.Timezone != nil {
		c.Timezone = strings.TrimSpace(*in.Timezone)
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}

	// Validate the final state, not just the changed fields.
	if err := validateCompany(*c); err != nil {
		return nil, err
	}

	return s.repo.Update(ctx, *c)
}

// Delete is for platform users only (same reasoning as Create).
func (s *CompanyService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if !p.IsPlatformUser() {
		return model.ErrForbidden
	}
	return s.repo.SoftDelete(ctx, id)
}

// checkCompanyScope enforces "a company user only sees their own company".
// Another company returns ErrNotFound, NOT ErrForbidden: a 403 would confirm
// that the id exists. Outside your tenant, nothing exists.
func checkCompanyScope(p auth.Principal, companyID uuid.UUID) error {
	if p.IsPlatformUser() || *p.CompanyID == companyID {
		return nil
	}
	return model.ErrNotFound
}

func validateCompany(c model.Company) error {
	var v model.Validator

	v.Check(c.Name != "", "name", "is required")
	v.Check(utf8.RuneCountInString(c.Name) <= companyNameMaxLen, "name", "must be at most 200 characters")

	v.Check(dotNumberRe.MatchString(c.DOTNumber), "dot_number", "must be 1 to 8 digits")

	v.Check(isValidTimezone(c.Timezone), "timezone", "must be a valid IANA time zone, e.g. America/Chicago")

	return v.Err()
}

// isValidTimezone accepts IANA names like "America/Chicago" or "UTC".
// time.LoadLocation treats "" as UTC and "Local" as the server's zone, so both
// are rejected explicitly: the zone must not depend on where the server runs.
func isValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// normalizePage applies defaults and limits so callers cannot request
// page 0 or a million rows at once.
func normalizePage(p ListParams) (page, size int) {
	page, size = p.Page, p.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	return page, size
}
