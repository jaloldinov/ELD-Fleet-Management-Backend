package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// DutyStatusStore stores logbook events (repository.DutyStatusRepository).
type DutyStatusStore interface {
	Insert(ctx context.Context, e model.DutyStatusEvent) (*model.DutyStatusEvent, error)
	At(ctx context.Context, driverID uuid.UUID, at time.Time) (*model.DutyStatusEvent, error)
}

// DutyDrivers finds and locks drivers (repository.DriverRepository).
type DutyDrivers interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
	LockByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
}

// DutyStints tells which truck a driver is on (repository.DriverAssignmentRepository).
type DutyStints interface {
	GetOpenByDriver(ctx context.Context, driverID uuid.UUID) (*model.DriverAssignment, error)
}

// DutyDevices gives a truck's ELD and its last reading (repository.ELDAssignmentRepository).
type DutyDevices interface {
	GetInstalled(ctx context.Context, truckID uuid.UUID) (*model.InstalledDevice, error)
}

// MaxDutyNoteLength matches the database CHECK.
const MaxDutyNoteLength = 500

// ChangeDutyStatusInput is a driver pressing a status button.
type ChangeDutyStatusInput struct {
	Status  model.DutyStatus
	Special *model.SpecialDriving // nil = none
	Note    *string
}

// DutyStatusService keeps drivers' logbooks.
//
// Not behind the billing gate: recording hours of service is required by
// law, also for a company whose payment failed.
type DutyStatusService struct {
	now     func() time.Time // our clock; replaceable in tests
	tx      Transactor
	events  DutyStatusStore
	drivers DutyDrivers
	stints  DutyStints
	devices DutyDevices
}

func NewDutyStatusService(tx Transactor, events DutyStatusStore, drivers DutyDrivers, stints DutyStints, devices DutyDevices) *DutyStatusService {
	return &DutyStatusService{now: time.Now, tx: tx, events: events, drivers: drivers, stints: stints, devices: devices}
}

// MyStatus returns the caller's status in force now, or nil if they have
// never recorded one. ErrNotFound if the caller is not a driver.
func (s *DutyStatusService) MyStatus(ctx context.Context, p auth.Principal) (*model.DutyStatusEvent, error) {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	e, err := s.events.At(ctx, dd.Driver.ID, s.now())
	if errors.Is(err, model.ErrNotFound) {
		return nil, nil
	}
	return e, err
}

// ChangeMyStatus records that the caller's status changes NOW (the server's
// clock: a driver cannot pick the time; fixing the past is an edit, 9e).
//
// Pressing the button of the status you are already in changes nothing:
// the current event comes back with created = false. A double tap or a
// retried request must not split one status into two rows.
func (s *DutyStatusService) ChangeMyStatus(ctx context.Context, p auth.Principal, in ChangeDutyStatusInput) (e *model.DutyStatusEvent, created bool, err error) {
	if err := validateDutyChange(&in); err != nil {
		return nil, false, err
	}
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		dd, err := s.drivers.GetByUserID(ctx, p.UserID)
		if err != nil {
			return err
		}
		// One change at a time per driver: two taps read the same "current"
		// otherwise, and both would write.
		if _, err := s.drivers.LockByID(ctx, dd.Driver.ID); err != nil {
			return err
		}
		// Whole seconds, like the FMCSA record (and a paper log).
		now := s.now().UTC().Truncate(time.Second)

		current, err := s.events.At(ctx, dd.Driver.ID, now)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return err
		}
		if current != nil && current.Status == in.Status && sameSpecial(current.Special, in.Special) {
			e = current
			return nil
		}

		ev := model.DutyStatusEvent{
			CompanyID: dd.Driver.CompanyID, DriverID: dd.Driver.ID,
			Status: in.Status, Special: in.Special, StartedAt: now,
			Origin: model.DutyOriginDriver, Note: in.Note, CreatedBy: &p.UserID,
		}
		if err := s.fillWhere(ctx, &ev); err != nil {
			return err
		}
		if ev.Special != nil && ev.TruckID == nil {
			return &validation.ValidationError{Fields: map[string]string{
				"special": "is driving a truck: you are on no truck"}}
		}
		e, err = s.events.Insert(ctx, ev)
		created = err == nil
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return e, created, nil
}

// fillWhere copies the truck the driver is on and that truck's last ELD
// reading (position, odometer, engine hours) into the event. Missing
// pieces stay nil: a driver at home has no truck, a new device has no
// reading yet.
func (s *DutyStatusService) fillWhere(ctx context.Context, ev *model.DutyStatusEvent) error {
	stint, err := s.stints.GetOpenByDriver(ctx, ev.DriverID)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	ev.TruckID = &stint.TruckID

	dev, err := s.devices.GetInstalled(ctx, stint.TruckID)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	st := dev.State
	// A reading the device took in ANOTHER truck (it was moved here and has
	// not reported since) says nothing about this one.
	if st == nil || st.TruckID == nil || *st.TruckID != stint.TruckID {
		return nil
	}
	ev.Latitude, ev.Longitude = st.Latitude, st.Longitude
	odo, hours := st.OdometerMi, st.EngineHours
	ev.OdometerMi, ev.EngineHours = &odo, &hours
	return nil
}

func validateDutyChange(in *ChangeDutyStatusInput) error {
	fields := map[string]string{}
	switch {
	case in.Status == model.DutyDriving:
		fields["status"] = "driving is recorded by the ELD from the truck's movement, not chosen"
	case !in.Status.Valid():
		fields["status"] = "must be off_duty, sleeper_berth or on_duty"
	case in.Special != nil && !in.Special.AllowedWith(in.Status):
		fields["special"] = "personal_conveyance goes with off_duty, yard_moves with on_duty"
	}
	if in.Note != nil {
		note := strings.TrimSpace(*in.Note)
		switch {
		case note == "":
			in.Note = nil // blank = no note
		case utf8.RuneCountInString(note) > MaxDutyNoteLength:
			fields["note"] = "must be at most 500 characters"
		default:
			in.Note = &note
		}
	}
	if len(fields) > 0 {
		return &validation.ValidationError{Fields: fields}
	}
	return nil
}

func sameSpecial(a, b *model.SpecialDriving) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
