package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Transactor runs fn in one database transaction (repository.TxManager).
// Every repository call made with the ctx given to fn joins that transaction.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// DriverUsers is the part of UserService that DriverService builds on.
// Reusing it means drivers get exactly the same login validation, password
// hashing and tenant rules as every other user.
type DriverUsers interface {
	Create(ctx context.Context, p auth.Principal, in CreateUserInput) (*UserDetails, error)
	Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error
}

// DriverRepository is what DriverService needs from the data layer.
type DriverRepository interface {
	Create(ctx context.Context, d model.Driver) (uuid.UUID, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
	List(ctx context.Context, f model.DriverFilter, limit, offset int) ([]model.DriverDetails, int, error)
	UpdateLicense(ctx context.Context, d model.Driver) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// Same rule as the drivers_license_number_format CHECK constraint.
var licenseNumberRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,25}$`)

// usStates: the 50 states + DC. (Canadian/Mexican licenses can be added when
// cross-border carriers become a requirement.)
var usStates = map[string]bool{
	"AL": true, "AK": true, "AZ": true, "AR": true, "CA": true, "CO": true, "CT": true, "DE": true,
	"DC": true, "FL": true, "GA": true, "HI": true, "ID": true, "IL": true, "IN": true, "IA": true,
	"KS": true, "KY": true, "LA": true, "ME": true, "MD": true, "MA": true, "MI": true, "MN": true,
	"MS": true, "MO": true, "MT": true, "NE": true, "NV": true, "NH": true, "NJ": true, "NM": true,
	"NY": true, "NC": true, "ND": true, "OH": true, "OK": true, "OR": true, "PA": true, "RI": true,
	"SC": true, "SD": true, "TN": true, "TX": true, "UT": true, "VT": true, "VA": true, "WA": true,
	"WV": true, "WI": true, "WY": true,
}

type CreateDriverInput struct {
	CompanyID *uuid.UUID // platform users: required; company users: nil or own
	Email     string     // optional: many drivers log in with a username
	Username  string
	Password  string
	FirstName string
	LastName  string

	LicenseNumber    string
	LicenseState     string
	LicenseExpiresAt time.Time // calendar day
}

// UpdateDriverInput changes license data only. Names and active status are
// user fields: change them with PATCH /users/{user_id}.
type UpdateDriverInput struct {
	LicenseNumber    *string
	LicenseState     *string
	LicenseExpiresAt *time.Time
}

type DriverListParams struct {
	CompanyID *uuid.UUID // platform users only; ignored for company users
	Page      int
	PageSize  int
}

type DriverPage struct {
	Items    []model.DriverDetails
	Total    int
	Page     int
	PageSize int
}

type DriverService struct {
	tx      Transactor
	users   DriverUsers
	drivers DriverRepository
}

func NewDriverService(tx Transactor, users DriverUsers, drivers DriverRepository) *DriverService {
	return &DriverService{tx: tx, users: users, drivers: drivers}
}

// Create makes a user with the driver role AND their driver profile, in one
// transaction: either both exist afterwards, or neither does.
func (s *DriverService) Create(ctx context.Context, p auth.Principal, in CreateDriverInput) (*model.DriverDetails, error) {
	// Drivers always belong to a company. Without this check a platform admin
	// would get the confusing error "role driver cannot be given to a
	// platform user".
	if p.IsPlatformUser() && in.CompanyID == nil {
		return nil, &model.ValidationError{Fields: map[string]string{"company_id": "is required"}}
	}

	// Validate the driver part BEFORE opening the transaction: no point
	// starting database work (and hashing a password) for invalid input.
	lic := model.Driver{
		LicenseNumber:    strings.TrimSpace(in.LicenseNumber),
		LicenseState:     strings.ToUpper(strings.TrimSpace(in.LicenseState)),
		LicenseExpiresAt: in.LicenseExpiresAt,
	}
	if err := validateLicense(lic); err != nil {
		return nil, err
	}

	var driverID uuid.UUID
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		// Same validation, tenant scope and hashing as any user.
		u, err := s.users.Create(ctx, p, CreateUserInput{
			CompanyID: in.CompanyID,
			Email:     in.Email,
			Username:  in.Username,
			Password:  in.Password,
			FirstName: in.FirstName,
			LastName:  in.LastName,
			Roles:     []string{model.RoleDriver},
		})
		if err != nil {
			return err
		}

		lic.UserID = u.User.ID
		lic.CompanyID = *u.User.CompanyID
		driverID, err = s.drivers.Create(ctx, lic)
		return err // an error here also rolls back the user insert
	})
	if err != nil {
		return nil, err
	}

	return s.drivers.GetByID(ctx, driverID)
}

func (s *DriverService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.DriverDetails, error) {
	return s.getInScope(ctx, p, id)
}

// Me returns the caller's own driver profile (for the driver app).
func (s *DriverService) Me(ctx context.Context, p auth.Principal) (*model.DriverDetails, error) {
	return s.drivers.GetByUserID(ctx, p.UserID) // ErrNotFound if the caller is not a driver
}

func (s *DriverService) List(ctx context.Context, p auth.Principal, params DriverListParams) (*DriverPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.DriverFilter{CompanyID: params.CompanyID}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID // tenant scope, applied in SQL
	}

	items, total, err := s.drivers.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &DriverPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func (s *DriverService) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateDriverInput) (*model.DriverDetails, error) {
	if in.LicenseNumber == nil && in.LicenseState == nil && in.LicenseExpiresAt == nil {
		return nil, &model.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	dd, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	d := dd.Driver
	if in.LicenseNumber != nil {
		d.LicenseNumber = strings.TrimSpace(*in.LicenseNumber)
	}
	if in.LicenseState != nil {
		d.LicenseState = strings.ToUpper(strings.TrimSpace(*in.LicenseState))
	}
	if in.LicenseExpiresAt != nil {
		d.LicenseExpiresAt = *in.LicenseExpiresAt
	}
	if err := validateLicense(d); err != nil {
		return nil, err
	}

	if err := s.drivers.UpdateLicense(ctx, d); err != nil {
		return nil, err
	}
	return s.drivers.GetByID(ctx, id)
}

// Delete removes the driver profile AND the driver's user account (and with
// it, all sessions) in one transaction.
func (s *DriverService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	dd, err := s.getInScope(ctx, p, id)
	if err != nil {
		return err
	}
	return s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.drivers.SoftDelete(ctx, id); err != nil {
			return err
		}
		return s.users.Delete(ctx, p, dd.Driver.UserID)
	})
}

// getInScope: drivers of other companies "do not exist" for company users.
func (s *DriverService) getInScope(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.DriverDetails, error) {
	dd, err := s.drivers.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && dd.Driver.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return dd, nil
}

func validateLicense(d model.Driver) error {
	var v model.Validator
	v.Check(licenseNumberRe.MatchString(d.LicenseNumber), "license_number",
		"must be 1-25 characters: letters, digits or hyphen")
	v.Check(usStates[d.LicenseState], "license_state", "must be a two-letter US state code, e.g. TX")
	v.Check(!d.LicenseExpiresAt.IsZero(), "license_expires_at", "is required")
	return v.Err()
}
