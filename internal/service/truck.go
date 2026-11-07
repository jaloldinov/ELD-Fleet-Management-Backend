package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
	"github.com/jaloldinov/ELD-learning/pkg/vin"
)

// TruckRepository is what TruckService needs from the data layer.
type TruckRepository interface {
	Create(ctx context.Context, t model.Truck) (*model.Truck, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
	LockByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
	List(ctx context.Context, f model.TruckFilter, limit, offset int) ([]model.Truck, int, error)
	Update(ctx context.Context, t model.Truck) (*model.Truck, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// TruckReleaser takes the drivers off a truck that leaves service
// (AssignmentService).
type TruckReleaser interface {
	ReleaseTruck(ctx context.Context, p auth.Principal, truckID uuid.UUID) error
}

var (
	unitNumberRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,20}$`)
	licensePlateRe = regexp.MustCompile(`^[A-Z0-9 -]{1,10}$`)
)

const truckTextMaxLen = 50

type CreateTruckInput struct {
	CompanyID    *uuid.UUID // platform users: required; company users: nil or own
	UnitNumber   string
	VIN          string
	Make         string
	Model        string
	Year         *int
	LicensePlate string // empty = no plate
	PlateState   string
}

// UpdateTruckInput: nil = unchanged. The VIN cannot be changed: it is the
// truck's identity (a wrong VIN means delete and re-create).
// To remove the plate, send LicensePlate and PlateState as "".
type UpdateTruckInput struct {
	UnitNumber   *string
	Make         *string
	Model        *string
	Year         *int
	LicensePlate *string
	PlateState   *string
	IsActive     *bool
}

type TruckListParams struct {
	CompanyID *uuid.UUID // platform users only
	IsActive  *bool
	Page      int
	PageSize  int
}

type TruckPage struct {
	Items    []model.Truck
	Total    int
	Page     int
	PageSize int
}

type TruckService struct {
	repo      TruckRepository
	drivers   TruckReleaser
	companies CompanyLookup
	audit     *Auditor
	now       func() time.Time // for the model-year rule; replaceable in tests

	// fleetChanged is told (after the commit) when a company's number of
	// active trucks may have changed, so per-truck billing can follow.
	// Never nil (a no-op until OnFleetChange).
	fleetChanged func(ctx context.Context, companyID uuid.UUID)
}

func NewTruckService(repo TruckRepository, drivers TruckReleaser, companies CompanyLookup, audit *Auditor) *TruckService {
	return &TruckService{repo: repo, drivers: drivers, companies: companies, audit: audit, now: time.Now,
		fleetChanged: func(context.Context, uuid.UUID) {}}
}

// OnFleetChange registers fn, called after a truck is added, removed,
// deactivated or reactivated. fn runs after the change is committed and
// cannot fail it: the truck change happened, billing catches up (and an
// hourly job catches up if this call fails).
func (s *TruckService) OnFleetChange(fn func(ctx context.Context, companyID uuid.UUID)) {
	s.fleetChanged = fn
}

func (s *TruckService) Create(ctx context.Context, p auth.Principal, in CreateTruckInput) (*model.Truck, error) {
	companyID, err := targetCompany(ctx, s.companies, p, in.CompanyID)
	if err != nil {
		return nil, err
	}

	t := model.Truck{
		CompanyID:  companyID,
		UnitNumber: strings.TrimSpace(in.UnitNumber),
		VIN:        vin.Normalize(in.VIN),
		Make:       strings.TrimSpace(in.Make),
		Model:      strings.TrimSpace(in.Model),
		Year:       in.Year,
	}
	t.LicensePlate, t.PlateState = normalizePlate(in.LicensePlate, in.PlateState)

	var v validation.Validator
	v.Check(vin.Valid(t.VIN), "vin", "must be a valid 17-character VIN (check digit included)")
	s.validateTruck(&v, t)
	if err := v.Err(); err != nil {
		return nil, err
	}

	var created *model.Truck
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.repo.Create(ctx, t); err != nil {
			return err
		}
		return s.record(ctx, p, "truck.create", created, truckChanges(model.Truck{}, *created))
	})
	if err != nil {
		return nil, err
	}
	s.fleetChanged(ctx, created.CompanyID)
	return created, nil
}

func (s *TruckService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Truck, error) {
	return s.getInScope(ctx, p, id)
}

func (s *TruckService) List(ctx context.Context, p auth.Principal, params TruckListParams) (*TruckPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.TruckFilter{CompanyID: params.CompanyID, IsActive: params.IsActive}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID // tenant scope, applied in SQL
	}

	items, total, err := s.repo.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &TruckPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func (s *TruckService) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateTruckInput) (*model.Truck, error) {
	if in == (UpdateTruckInput{}) {
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	t, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	before := *t

	if in.UnitNumber != nil {
		t.UnitNumber = strings.TrimSpace(*in.UnitNumber)
	}
	if in.Make != nil {
		t.Make = strings.TrimSpace(*in.Make)
	}
	if in.Model != nil {
		t.Model = strings.TrimSpace(*in.Model)
	}
	if in.Year != nil {
		t.Year = in.Year
	}
	if in.LicensePlate != nil || in.PlateState != nil {
		// The plate and its state change together.
		plate, state := deref(t.LicensePlate), deref(t.PlateState)
		if in.LicensePlate != nil {
			plate = *in.LicensePlate
		}
		if in.PlateState != nil {
			state = *in.PlateState
		}
		t.LicensePlate, t.PlateState = normalizePlate(plate, state)
	}
	if in.IsActive != nil {
		t.IsActive = *in.IsActive
	}

	var v validation.Validator
	s.validateTruck(&v, *t)
	if err := v.Err(); err != nil {
		return nil, err
	}

	var updated *model.Truck
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		// Taken out of service: nobody drives it any more.
		if before.IsActive && !t.IsActive {
			if err := s.releaseDrivers(ctx, p, id); err != nil {
				return err
			}
		}
		var err error
		if updated, err = s.repo.Update(ctx, *t); err != nil {
			return err
		}
		if ch := truckChanges(before, *updated); len(ch) > 0 {
			return s.record(ctx, p, "truck.update", updated, ch)
		}
		return nil // nothing really changed: nothing to record
	})
	if err != nil {
		return nil, err
	}
	if before.IsActive != updated.IsActive {
		s.fleetChanged(ctx, updated.CompanyID)
	}
	return updated, nil
}

func (s *TruckService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	t, err := s.getInScope(ctx, p, id)
	if err != nil {
		return err
	}
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.releaseDrivers(ctx, p, id); err != nil {
			return err
		}
		if err := s.repo.SoftDelete(ctx, id); err != nil {
			return err
		}
		return s.record(ctx, p, "truck.delete", t, nil)
	})
	if err != nil {
		return err
	}
	if t.IsActive {
		s.fleetChanged(ctx, t.CompanyID)
	}
	return nil
}

// releaseDrivers ends the stints on a truck leaving service. The truck is
// LOCKED first, in this transaction: an assignment to it that is in
// progress finishes before we look (so its stint is ended too), and one
// that starts later waits and then finds the truck gone or inactive.
func (s *TruckService) releaseDrivers(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if _, err := s.repo.LockByID(ctx, id); err != nil {
		return err
	}
	return s.drivers.ReleaseTruck(ctx, p, id)
}

func (s *TruckService) record(ctx context.Context, p auth.Principal, action string, t *model.Truck, ch model.AuditChanges) error {
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditTruck, EntityID: t.ID.String(), CompanyID: &t.CompanyID, Changes: ch,
	})
}

// truckChanges lists the audited fields that differ (API field names).
// For a create, old is the zero Truck.
func truckChanges(old, new model.Truck) model.AuditChanges {
	c := model.AuditChanges{}
	c.Set("unit_number", old.UnitNumber, new.UnitNumber)
	c.Set("vin", old.VIN, new.VIN)
	c.Set("make", old.Make, new.Make)
	c.Set("model", old.Model, new.Model)
	c.Set("year", old.Year, new.Year)
	c.Set("license_plate", old.LicensePlate, new.LicensePlate)
	c.Set("plate_state", old.PlateState, new.PlateState)
	c.Set("is_active", old.IsActive, new.IsActive)
	return c
}

// targetCompany decides which company a new fleet record (truck, ELD
// device...) belongs to: the caller's own company for company users (from
// the token), a required and existing company for platform users.
func targetCompany(ctx context.Context, companies CompanyLookup, p auth.Principal, requested *uuid.UUID) (uuid.UUID, error) {
	if !p.IsPlatformUser() {
		if requested != nil && *requested != *p.CompanyID {
			return uuid.Nil, model.ErrNotFound
		}
		return *p.CompanyID, nil
	}
	if requested == nil {
		return uuid.Nil, &validation.ValidationError{Fields: map[string]string{"company_id": "is required"}}
	}
	if _, err := companies.GetByID(ctx, *requested); errors.Is(err, model.ErrNotFound) {
		return uuid.Nil, &validation.ValidationError{Fields: map[string]string{"company_id": "company does not exist"}}
	} else if err != nil {
		return uuid.Nil, err
	}
	return *requested, nil
}

func (s *TruckService) getInScope(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Truck, error) {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && t.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound // outside your tenant, nothing exists
	}
	return t, nil
}

func (s *TruckService) validateTruck(v *validation.Validator, t model.Truck) {
	v.Check(unitNumberRe.MatchString(t.UnitNumber), "unit_number",
		"must be 1-20 characters: letters, digits or hyphen")
	v.Check(utf8.RuneCountInString(t.Make) <= truckTextMaxLen, "make", "must be at most 50 characters")
	v.Check(utf8.RuneCountInString(t.Model) <= truckTextMaxLen, "model", "must be at most 50 characters")
	if t.Year != nil {
		// VINs as we know them start with model year 1981; a model year can
		// be at most one year ahead of the calendar.
		maxYear := s.now().Year() + 1
		v.Check(*t.Year >= 1981 && *t.Year <= maxYear, "year", "must be between 1981 and next year")
	}
	if (t.LicensePlate == nil) != (t.PlateState == nil) {
		v.Check(false, "license_plate", "license_plate and plate_state must be given together")
	} else if t.LicensePlate != nil {
		v.Check(licensePlateRe.MatchString(*t.LicensePlate), "license_plate", "must be 1-10 characters: letters, digits, space or hyphen")
		v.Check(validation.USState(*t.PlateState), "plate_state", "must be a two-letter US state code, e.g. TX")
	}
}

// normalizePlate upper-cases and trims; empty values become nil.
func normalizePlate(plate, state string) (*string, *string) {
	plate = strings.ToUpper(strings.TrimSpace(plate))
	state = strings.ToUpper(strings.TrimSpace(state))
	return nilIfEmpty(plate), nilIfEmpty(state)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
