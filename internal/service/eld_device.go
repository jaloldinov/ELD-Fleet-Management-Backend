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
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// ELDDeviceRepository is what ELDDeviceService needs from the data layer.
type ELDDeviceRepository interface {
	Create(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
	List(ctx context.Context, f model.ELDDeviceFilter, limit, offset int) ([]model.ELDDevice, int, error)
	Update(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error)
	LockByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
	SetKey(ctx context.Context, id uuid.UUID, hash []byte) (*model.ELDDevice, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// DeviceStates reads a device's last known reading (repository.TelemetryRepository).
type DeviceStates interface {
	GetState(ctx context.Context, deviceID uuid.UUID) (*model.DeviceState, error)
}

// DeviceReleaser takes a device that is being deleted out of its truck
// (AssignmentService, which also installs and removes devices).
type DeviceReleaser interface {
	ReleaseDevice(ctx context.Context, p auth.Principal, deviceID uuid.UUID) error
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
// (it is the device's identity); the truck is changed with
// AssignmentService.InstallDevice / RemoveDevice.
type UpdateELDDeviceInput struct {
	Model           *string
	FirmwareVersion *string
	Status          *model.ELDStatus
	// IsActive is the pre-status way: true = active, false = retired. Kept
	// for old clients; may be sent with Status only if they agree.
	IsActive *bool
}

type ELDDeviceListParams struct {
	CompanyID *uuid.UUID // platform users only
	IsActive  *bool
	Status    *model.ELDStatus
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
	installs  DeviceReleaser
	states    DeviceStates
	companies CompanyLookup
	audit     *Auditor
}

func NewELDDeviceService(repo ELDDeviceRepository, installs DeviceReleaser, states DeviceStates, companies CompanyLookup, audit *Auditor) *ELDDeviceService {
	return &ELDDeviceService{repo: repo, installs: installs, states: states, companies: companies, audit: audit}
}

// State returns the device and its last known reading (nil if it has sent
// none). Online/offline follows from the device's LastSeenAt.
func (s *ELDDeviceService) State(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, *model.DeviceState, error) {
	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, nil, err
	}
	st, err := s.states.GetState(ctx, id)
	if errors.Is(err, model.ErrNotFound) {
		return d, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	return d, st, nil
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

	var v validation.Validator
	v.Check(serialNumberRe.MatchString(d.SerialNumber), "serial_number",
		"must be 4-32 characters: letters, digits or hyphen")
	validateELDText(&v, d)
	if err := v.Err(); err != nil {
		return nil, err
	}

	return s.write(ctx, p, "eld_device.create", model.ELDDevice{}, func(ctx context.Context) (*model.ELDDevice, error) {
		return s.repo.Create(ctx, d)
	})
}

func (s *ELDDeviceService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error) {
	return s.getInScope(ctx, p, id)
}

func (s *ELDDeviceService) List(ctx context.Context, p auth.Principal, params ELDDeviceListParams) (*ELDDevicePage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.ELDDeviceFilter{CompanyID: params.CompanyID, IsActive: params.IsActive, Status: params.Status, TruckID: params.TruckID}
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
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	before := *d

	if in.Model != nil {
		d.Model = strings.TrimSpace(*in.Model)
	}
	if in.FirmwareVersion != nil {
		d.FirmwareVersion = strings.TrimSpace(*in.FirmwareVersion)
	}
	var v validation.Validator
	if st, ok := eldStatusFrom(&v, in.Status, in.IsActive); ok {
		d.Status = st
	}
	validateELDText(&v, *d)
	// A retired device in a truck would leave the truck "with an ELD" that
	// will never record again. Make the user take it out first, explicitly.
	// A faulty one may stay until it is replaced (PUT .../truck, replace).
	field := "status"
	if in.Status == nil {
		field = "is_active" // what the client sent
	}
	v.Check(d.Status != model.ELDRetired || d.TruckID == nil, field, "remove the device from its truck first")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return s.write(ctx, p, "eld_device.update", before, func(ctx context.Context) (*model.ELDDevice, error) {
		return s.repo.Update(ctx, *d)
	})
}

// IssueKey gives the device a new API key and returns it. This is the only
// time the key is ever visible: only its hash is stored, so a lost key
// cannot be shown again, only replaced. Issuing again rotates: the old key
// stops working at once (e.g. the device was stolen, or the key leaked).
func (s *ELDDeviceService) IssueKey(ctx context.Context, p auth.Principal, id uuid.UUID) (string, *model.ELDDevice, error) {
	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return "", nil, err
	}
	if d.Status == model.ELDRetired {
		return "", nil, &validation.ValidationError{Fields: map[string]string{"status": "a retired device gets no key"}}
	}
	plain, hash, err := auth.NewDeviceKey()
	if err != nil {
		return "", nil, err
	}
	action := "eld_device.issue_key"
	if d.KeyCreatedAt != nil {
		action = "eld_device.rotate_key"
	}
	var updated *model.ELDDevice
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = s.repo.SetKey(ctx, id, hash); err != nil {
			return err
		}
		// Never the key itself (nor its hash) in the audit log.
		return s.recordKey(ctx, p, action, *updated)
	})
	if err != nil {
		return "", nil, err
	}
	return plain, updated, nil
}

// RevokeKey removes the device's key: it can no longer call the API until
// a new key is issued. A device without a key is not an error.
func (s *ELDDeviceService) RevokeKey(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error) {
	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if d.KeyCreatedAt == nil {
		return d, nil // nothing to revoke, nothing to record
	}
	var updated *model.ELDDevice
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = s.repo.SetKey(ctx, id, nil); err != nil {
			return err
		}
		return s.recordKey(ctx, p, "eld_device.revoke_key", *updated)
	})
	return updated, err
}

func (s *ELDDeviceService) recordKey(ctx context.Context, p auth.Principal, action string, d model.ELDDevice) error {
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditELDDevice, EntityID: d.ID.String(), CompanyID: &d.CompanyID,
	})
}

func (s *ELDDeviceService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	d, err := s.getInScope(ctx, p, id)
	if err != nil {
		return err
	}
	return s.audit.WithinTx(ctx, func(ctx context.Context) error {
		// Lock, take it out of its truck, delete: an install in progress
		// finishes first, and its stint is ended too.
		if _, err := s.repo.LockByID(ctx, id); err != nil {
			return err
		}
		if err := s.installs.ReleaseDevice(ctx, p, id); err != nil {
			return err
		}
		if err := s.repo.SoftDelete(ctx, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: "eld_device.delete", Entity: model.AuditELDDevice, EntityID: id.String(), CompanyID: &d.CompanyID,
		})
	})
}

// write runs change and records what it changed compared with before, in
// one transaction. Nothing is recorded if nothing changed (e.g. removing a
// device that was not installed).
func (s *ELDDeviceService) write(ctx context.Context, p auth.Principal, action string, before model.ELDDevice,
	change func(ctx context.Context) (*model.ELDDevice, error)) (*model.ELDDevice, error) {
	var after *model.ELDDevice
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if after, err = change(ctx); err != nil {
			return err
		}
		ch := eldDeviceChanges(before, *after)
		if len(ch) == 0 {
			return nil
		}
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: action, Entity: model.AuditELDDevice, EntityID: after.ID.String(), CompanyID: &after.CompanyID, Changes: ch,
		})
	})
	return after, err
}

// eldDeviceChanges lists the audited fields that differ (API field names).
func eldDeviceChanges(old, new model.ELDDevice) model.AuditChanges {
	c := model.AuditChanges{}
	c.Set("serial_number", old.SerialNumber, new.SerialNumber)
	c.Set("model", old.Model, new.Model)
	c.Set("firmware_version", old.FirmwareVersion, new.FirmwareVersion)
	c.Set("truck_id", old.TruckID, new.TruckID)
	c.Set("status", old.Status, new.Status) // is_active follows from it
	return c
}

// eldStatusFrom: like truckStatusFrom, for devices (false = retired).
func eldStatusFrom(v *validation.Validator, status *model.ELDStatus, isActive *bool) (model.ELDStatus, bool) {
	var st model.ELDStatus
	switch {
	case status != nil:
		st = *status
		if !st.Valid() {
			v.Check(false, "status", "must be active, faulty or retired")
			return "", false
		}
		if isActive != nil && *isActive != (st == model.ELDActive) {
			v.Check(false, "is_active", "contradicts status (send only status)")
			return "", false
		}
	case isActive != nil:
		st = model.ELDRetired
		if *isActive {
			st = model.ELDActive
		}
	default:
		return "", false
	}
	return st, true
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

func validateELDText(v *validation.Validator, d model.ELDDevice) {
	v.Check(utf8.RuneCountInString(d.Model) <= eldTextMaxLen, "model", "must be at most 50 characters")
	v.Check(utf8.RuneCountInString(d.FirmwareVersion) <= eldTextMaxLen, "firmware_version", "must be at most 50 characters")
}
