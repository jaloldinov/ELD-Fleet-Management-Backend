package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// ELDDeviceRepository is what ELDDeviceService needs from the data layer.
type ELDDeviceRepository interface {
	Create(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
	List(ctx context.Context, f model.ELDDeviceFilter, limit, offset int) ([]model.ELDDevice, int, error)
	Update(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error)
	SetTruck(ctx context.Context, id uuid.UUID, truckID *uuid.UUID) (*model.ELDDevice, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// TruckLookup finds the truck a device is being installed in.
type TruckLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
}

// Same rule as the eld_devices_serial_format CHECK constraint.
var serialNumberRe = regexp.MustCompile(`^[A-Z0-9-]{4,32}$`)

const eldTextMaxLen = 50

type CreateELDDeviceInput struct {
	CompanyID       *uuid.UUID // platform users: required; company users: nil or own
	SerialNumber    string
	Model           string
	FirmwareVersion string
}

// UpdateELDDeviceInput: nil = unchanged. The serial number cannot be changed
// (it is the device's identity); the truck is changed with AssignTruck.
type UpdateELDDeviceInput struct {
	Model           *string
	FirmwareVersion *string
	IsActive        *bool
}

type ELDDeviceListParams struct {
	CompanyID *uuid.UUID // platform users only
	IsActive  *bool
	TruckID   *uuid.UUID
	Page      int
	PageSize  int
}

type ELDDevicePage struct {
	Items    []model.ELDDevice
	Total    int
	Page     int
	PageSize int
}

type ELDDeviceService struct {
	repo      ELDDeviceRepository
	trucks    TruckLookup
	companies CompanyLookup
}

func NewELDDeviceService(repo ELDDeviceRepository, trucks TruckLookup, companies CompanyLookup) *ELDDeviceService {
	return &ELDDeviceService{repo: repo, trucks: trucks, companies: companies}
}

func (s *ELDDeviceService) Create(ctx context.Context, p auth.Principal, in CreateELDDeviceInput) (*model.ELDDevice, error) {
	companyID, err := targetCompany(ctx, s.companies, p, in.CompanyID)
	if err != nil {
		return nil, err
	}

	d := model.ELDDevice{
		CompanyID:       companyID,
		SerialNumber:    strings.ToUpper(strings.TrimSpace(in.SerialNumber)),
		Model:           strings.TrimSpace(in.Model),
		FirmwareVersion: strings.TrimSpace(in.FirmwareVersion),
	}

	var v model.Validator
	v.Check(serialNumberRe.MatchString(d.SerialNumber), "serial_number",
		"must be 4-32 characters: letters, digits or hyphen")
	validateELDText(&v, d)
	if err := v.Err(); err != nil {
		return nil, err
	}

	return s.repo.Create(ctx, d)
}

func (s *ELDDeviceService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error) {
	return s.getInScope(ctx, p, id)
}

func (s *ELDDeviceService) List(ctx context.Context, p auth.Principal, params ELDDeviceListParams) (*ELDDevicePage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.ELDDeviceFilter{CompanyID: params.CompanyID, IsActive: params.IsActive, TruckID: params.TruckID}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID // tenant scope, applied in SQL
	}

	items, total, err := s.repo.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &ELDDevicePage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func (s *ELDDeviceService) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateELDDeviceInput) (*model.ELDDevice, error) {
	if in == (UpdateELDDeviceInput{}) {
		return nil, &model.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}

	if in.Model != nil {
		d.Model = strings.TrimSpace(*in.Model)
	}
	if in.FirmwareVersion != nil {
		d.FirmwareVersion = strings.TrimSpace(*in.FirmwareVersion)
	}
	if in.IsActive != nil {
		d.IsActive = *in.IsActive
	}

	var v model.Validator
	validateELDText(&v, *d)
	// An inactive device in a truck would leave the truck "with an ELD" that
	// records nothing. Make the user take it out first, explicitly.
	v.Check(d.IsActive || d.TruckID == nil, "is_active", "remove the device from its truck first")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return s.repo.Update(ctx, *d)
}

// AssignTruck installs the device in a truck of the same company, or moves it
// there from its current truck.
func (s *ELDDeviceService) AssignTruck(ctx context.Context, p auth.Principal, id, truckID uuid.UUID) (*model.ELDDevice, error) {
	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !d.IsActive {
		return nil, &model.ValidationError{Fields: map[string]string{"is_active": "an inactive device cannot be installed"}}
	}

	t, err := s.trucks.GetByID(ctx, truckID)
	// Another company's truck gets the same answer as a missing one, so
	// truck IDs of other tenants cannot be probed. Also checked by the
	// composite foreign key in the database.
	if errors.Is(err, model.ErrNotFound) || (err == nil && t.CompanyID != d.CompanyID) {
		return nil, &model.ValidationError{Fields: map[string]string{"truck_id": "truck not found"}}
	} else if err != nil {
		return nil, err
	}
	if !t.IsActive {
		return nil, &model.ValidationError{Fields: map[string]string{"truck_id": "truck is out of service"}}
	}

	return s.repo.SetTruck(ctx, id, &truckID) // ErrTruckHasELD if the truck is taken
}

// UnassignTruck removes the device from its truck. Removing a device that is
// not installed is not an error: the result is the same.
func (s *ELDDeviceService) UnassignTruck(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error) {
	if _, err := s.getInScope(ctx, p, id); err != nil {
		return nil, err
	}
	return s.repo.SetTruck(ctx, id, nil)
}

func (s *ELDDeviceService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if _, err := s.getInScope(ctx, p, id); err != nil {
		return err
	}
	return s.repo.SoftDelete(ctx, id)
}

func (s *ELDDeviceService) getInScope(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error) {
	d, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && d.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound // outside your tenant, nothing exists
	}
	return d, nil
}

func validateELDText(v *model.Validator, d model.ELDDevice) {
	v.Check(utf8.RuneCountInString(d.Model) <= eldTextMaxLen, "model", "must be at most 50 characters")
	v.Check(utf8.RuneCountInString(d.FirmwareVersion) <= eldTextMaxLen, "firmware_version", "must be at most 50 characters")
}
