package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	ListPeriod(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error)
	ListReplaced(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.DutyStatusEvent, error)
	Inactivate(ctx context.Context, id uuid.UUID, by uuid.UUID) error
	Certify(ctx context.Context, c model.LogCertification) (*model.LogCertification, error)
	LastCertification(ctx context.Context, driverID uuid.UUID, date string) (*time.Time, error)
}

// DutyDrivers finds and locks drivers (repository.DriverRepository).
type DutyDrivers interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
	LockByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
}

// DutyStints tells which truck a driver is on (repository.DriverAssignmentRepository).
type DutyStints interface {
	GetOpenByDriver(ctx context.Context, driverID uuid.UUID) (*model.DriverAssignment, error)
	PrimaryAt(ctx context.Context, truckID uuid.UUID, at time.Time) (*model.DriverAssignment, error)
}

// DutyDevices gives a truck's ELD and its last reading, and which truck a
// device was in (repository.ELDAssignmentRepository).
type DutyDevices interface {
	GetInstalled(ctx context.Context, truckID uuid.UUID) (*model.InstalledDevice, error)
	TruckAt(ctx context.Context, deviceID uuid.UUID, at time.Time) (uuid.UUID, error)
}

// DutyCompanies gives a company's home-terminal time zone (repository.CompanyRepository).
type DutyCompanies interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
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
	now       func() time.Time // our clock; replaceable in tests
	tx        Transactor
	events    DutyStatusStore
	drivers   DutyDrivers
	stints    DutyStints
	devices   DutyDevices
	companies DutyCompanies
}

func NewDutyStatusService(tx Transactor, events DutyStatusStore, drivers DutyDrivers, stints DutyStints, devices DutyDevices, companies DutyCompanies) *DutyStatusService {
	return &DutyStatusService{now: time.Now, tx: tx, events: events, drivers: drivers, stints: stints, devices: devices, companies: companies}
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
		if err := s.fillWhere(ctx, &ev, now); err != nil {
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
//
// It also refuses the change while the driver's truck is moving with them
// at the wheel (ErrTruckMoving): the ELD has them driving, and FMCSA does
// not let a driver work the ELD in motion. A co-driver may.
func (s *DutyStatusService) fillWhere(ctx context.Context, ev *model.DutyStatusEvent, now time.Time) error {
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
	// A reading older than DeviceOfflineAfter says nothing about now (the
	// device lost power or signal while moving).
	if stint.Role == model.AssignmentPrimary && st.Moving && now.Sub(st.RecordedAt) <= model.DeviceOfflineAfter {
		return model.ErrTruckMoving
	}
	ev.Latitude, ev.Longitude = st.Latitude, st.Longitude
	odo, hours := st.OdometerMi, st.EngineHours
	ev.OdometerMi, ev.EngineHours = &odo, &hours
	return nil
}

// MyDailyLog is the caller's own log of a day (YYYY-MM-DD, in the
// company's time zone). ErrNotFound if the caller is not a driver.
func (s *DutyStatusService) MyDailyLog(ctx context.Context, p auth.Principal, date string) (*model.DailyLog, error) {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return s.dailyLog(ctx, dd.Driver, date)
}

// DriverDailyLog is a driver's log of a day, for the office. Drivers of
// other companies "do not exist" for company users (ErrNotFound).
func (s *DutyStatusService) DriverDailyLog(ctx context.Context, p auth.Principal, driverID uuid.UUID, date string) (*model.DailyLog, error) {
	dd, err := s.drivers.GetByID(ctx, driverID)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && dd.Driver.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return s.dailyLog(ctx, dd.Driver, date)
}

func (s *DutyStatusService) dailyLog(ctx context.Context, d model.Driver, date string) (*model.DailyLog, error) {
	company, err := s.companies.GetByID(ctx, d.CompanyID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(company.Timezone)
	if err != nil {
		return nil, fmt.Errorf("company %s time zone %q: %w", company.ID, company.Timezone, err)
	}
	start, end, err := model.DayBounds(date, loc)
	if err != nil {
		return nil, &validation.ValidationError{Fields: map[string]string{"date": "must be a date like 2026-11-10"}}
	}
	now := s.now()
	if !start.Before(now) {
		return nil, &validation.ValidationError{Fields: map[string]string{"date": "is in the future"}}
	}

	events, err := s.events.ListPeriod(ctx, d.ID, start, end)
	if err != nil {
		return nil, err
	}
	log := model.BuildDailyLog(events, start, end, now)
	log.DriverID, log.Date, log.Location = d.ID, date, loc

	replaced, err := s.events.ListReplaced(ctx, d.ID, start, end)
	if err != nil {
		return nil, err
	}
	log.SetHistory(replaced)
	if log.CertifiedAt, err = s.events.LastCertification(ctx, d.ID, date); err != nil {
		return nil, err
	}

	// The day's violations: the hours-of-service engine run up to the end
	// of the day (or now), over enough log before it to know the shift and
	// the cycle the day began in.
	hosFrom := log.Until.Add(-model.HOSLookback)
	hosEvents, err := s.events.ListPeriod(ctx, d.ID, hosFrom, log.Until)
	if err != nil {
		return nil, err
	}
	log.SetViolations(model.EvaluateHOS(hosEvents, hosFrom, log.Until, loc, company.HOSCycle).Violations)
	return &log, nil
}

// ---------- edits and certification (the driver fixes and signs) ----------

// DutyRecordInput is a record as the driver says it should be: a record
// they forgot to enter, or the new version of one they got wrong.
type DutyRecordInput struct {
	Status    model.DutyStatus
	Special   *model.SpecialDriving
	StartedAt time.Time
	Note      *string
	Reason    string // why the log is being fixed (required)
}

// AddMyRecord enters a record the driver forgot ("I went off duty at 18:00
// but did not press the button"). The rules of ChangeMyRecord apply.
func (s *DutyStatusService) AddMyRecord(ctx context.Context, p auth.Principal, in DutyRecordInput) (*model.DutyStatusEvent, error) {
	now := s.now().UTC()
	if err := validateDutyRecord(&in, now); err != nil {
		return nil, err
	}
	var created *model.DutyStatusEvent
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		d, err := s.lockMe(ctx, p)
		if err != nil {
			return err
		}
		e := s.editedRecord(d, p, in)
		if err := s.keepsDriving(ctx, d.ID, nil, e, now); err != nil {
			return err
		}
		created, err = s.events.Insert(ctx, e)
		return err
	})
	return created, err
}

// ChangeMyRecord replaces one of the driver's records with a corrected
// version. The old one is not changed or deleted: it becomes inactive
// history, and the new one points to it (FMCSA keeps every version).
//
// Rules:
//   - only the newest version of the driver's own record (ErrRecordReplaced);
//   - never a driving record (ErrDrivingNotEditable), and never so that
//     driving changes: moving a record into, out of or next to a drive
//     shortens or stretches it (ErrEditChangesDriving);
//   - within the last 8 days, never in the future;
//   - always with a reason.
func (s *DutyStatusService) ChangeMyRecord(ctx context.Context, p auth.Principal, id uuid.UUID, in DutyRecordInput) (*model.DutyStatusEvent, error) {
	now := s.now().UTC()
	if err := validateDutyRecord(&in, now); err != nil {
		return nil, err
	}
	var created *model.DutyStatusEvent
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		d, err := s.lockMe(ctx, p)
		if err != nil {
			return err
		}
		old, err := s.events.GetByID(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case old.DriverID != d.ID:
			return model.ErrNotFound // another driver's record "does not exist"
		case old.RecordStatus == model.RecordInactive:
			return model.ErrRecordReplaced
		case old.Status == model.DutyDriving:
			return model.ErrDrivingNotEditable
		case old.StartedAt.Before(now.Add(-model.DutyEditWindow)):
			return &validation.ValidationError{Fields: map[string]string{"id": "is older than 8 days: that part of the log is settled"}}
		}

		e := s.editedRecord(d, p, in)
		if e.Status == old.Status && sameSpecial(e.Special, old.Special) && e.StartedAt.Equal(old.StartedAt) && sameNote(e.Note, old.Note) {
			return &validation.ValidationError{Fields: map[string]string{"status": "the record already says this: nothing to change"}}
		}
		e.Replaces = &old.ID
		if e.StartedAt.Equal(old.StartedAt) { // same moment: same place
			e.TruckID, e.Latitude, e.Longitude, e.OdometerMi, e.EngineHours =
				old.TruckID, old.Latitude, old.Longitude, old.OdometerMi, old.EngineHours
		}
		if err := s.keepsDriving(ctx, d.ID, old, e, now); err != nil {
			return err
		}
		// Old out first: the new version may start at the same instant,
		// and only one ACTIVE record may.
		if err := s.events.Inactivate(ctx, old.ID, p.UserID); err != nil {
			return err
		}
		created, err = s.events.Insert(ctx, e)
		return err
	})
	return created, err
}

// CertifyMyLog signs a day (YYYY-MM-DD): "my record of duty status for this
// 24-hour period is true and correct". A day already signed and unchanged
// since is not signed twice (certified = false). A day changed after its
// signature (an edit, or a record entered later) needs signing again.
func (s *DutyStatusService) CertifyMyLog(ctx context.Context, p auth.Principal, date string) (log *model.DailyLog, certified bool, err error) {
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		d, err := s.lockMe(ctx, p)
		if err != nil {
			return err
		}
		if log, err = s.dailyLog(ctx, *d, date); err != nil {
			return err
		}
		if len(log.Segments) == 0 {
			return &validation.ValidationError{Fields: map[string]string{"date": "has no records to certify"}}
		}
		if log.Certified() {
			return nil
		}
		c, err := s.events.Certify(ctx, model.LogCertification{
			CompanyID: d.CompanyID, DriverID: d.ID, Date: date, CertifiedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		log.CertifiedAt, certified = &c.CertifiedAt, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return log, certified, nil
}

// lockMe finds the caller's driver profile and locks it: one change of a
// driver's log at a time (edits, buttons, the ELD).
func (s *DutyStatusService) lockMe(ctx context.Context, p auth.Principal) (*model.Driver, error) {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if _, err := s.drivers.LockByID(ctx, dd.Driver.ID); err != nil {
		return nil, err
	}
	return &dd.Driver, nil
}

func (s *DutyStatusService) editedRecord(d *model.Driver, p auth.Principal, in DutyRecordInput) model.DutyStatusEvent {
	reason := in.Reason
	return model.DutyStatusEvent{
		CompanyID: d.CompanyID, DriverID: d.ID, Status: in.Status, Special: in.Special, StartedAt: in.StartedAt,
		Origin: model.DutyOriginDriver, Note: in.Note, CreatedBy: &p.UserID, EditReason: &reason,
	}
}

// keepsDriving refuses an edit that would change when the driver was
// driving: it compares the driving intervals from the edit on, before and
// after (old: the record replaced; nil when adding). One rule covers every
// way to cheat: a record inside a drive, moving the record that ends a
// drive, relabelling around one.
//
// It also refuses a second active record at the same instant
// (ErrDutyStatusTaken), before the database would.
func (s *DutyStatusService) keepsDriving(ctx context.Context, driverID uuid.UUID, old *model.DutyStatusEvent, e model.DutyStatusEvent, now time.Time) error {
	from := e.StartedAt
	if old != nil && old.StartedAt.Before(from) {
		from = old.StartedAt
	}
	// A microsecond earlier (the database's precision): the record in force
	// just BEFORE the edit is part of the picture. Without it, removing the
	// record that ended a drive would look harmless.
	from = from.Add(-time.Microsecond)

	before, err := s.events.ListPeriod(ctx, driverID, from, now)
	if err != nil {
		return err
	}
	after := make([]model.DutyStatusEvent, 0, len(before)+1)
	for _, b := range before {
		if old != nil && b.ID == old.ID {
			continue
		}
		if b.StartedAt.Equal(e.StartedAt) {
			return model.ErrDutyStatusTaken
		}
		after = append(after, b)
	}
	after = append(after, e)
	sort.SliceStable(after, func(i, j int) bool { return after[i].StartedAt.Before(after[j].StartedAt) })

	if !model.SameIntervals(model.DrivingIntervals(before, from, now), model.DrivingIntervals(after, from, now)) {
		return model.ErrEditChangesDriving
	}
	return nil
}

func validateDutyRecord(in *DutyRecordInput, now time.Time) error {
	change := ChangeDutyStatusInput{Status: in.Status, Special: in.Special, Note: in.Note}
	fields := map[string]string{}
	var vErr *validation.ValidationError
	if err := validateDutyChange(&change); errors.As(err, &vErr) {
		fields = vErr.Fields
	}
	in.Note = change.Note

	in.StartedAt = in.StartedAt.UTC().Truncate(time.Second)
	switch {
	case in.StartedAt.IsZero():
		fields["started_at"] = "is required"
	case in.StartedAt.After(now):
		fields["started_at"] = "is in the future"
	case in.StartedAt.Before(now.Add(-model.DutyEditWindow)):
		fields["started_at"] = "is older than 8 days: that part of the log is settled"
	}

	in.Reason = strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(in.Reason); n < model.MinEditReasonLength || n > model.MaxEditReasonLength {
		fields["reason"] = "say why, in 4 to 60 characters"
	}
	if len(fields) > 0 {
		return &validation.ValidationError{Fields: fields}
	}
	return nil
}

func sameNote(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// RecordMotion writes what a truck's movement means for the driver at its
// wheel (the primary driver then; a co-driver is never driving):
//
//   - DriveStarted: they are driving from c.At, unless already driving or
//     in personal conveyance / yard moves (moving is what those are for).
//   - StopConfirmed: a driver still in the ELD's driving becomes on duty
//     at c.At. A driver who chose a status meanwhile (stopped, pressed
//     off duty) is left alone.
//
// Nothing is written when the device was in no truck or nobody was at the
// wheel. (FMCSA keeps that "unidentified driving" for a driver to claim
// later; we do not record it yet.)
//
// Runs inside the caller's transaction (Ingest, SettleStops), and is safe
// to repeat: the decision comes from the logbook itself.
func (s *DutyStatusService) RecordMotion(ctx context.Context, deviceID uuid.UUID, c model.MotionChange) error {
	truckID, err := s.devices.TruckAt(ctx, deviceID, c.At)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	stint, err := s.stints.PrimaryAt(ctx, truckID, c.At)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := s.drivers.LockByID(ctx, stint.DriverID); errors.Is(err, model.ErrNotFound) {
		return nil // the driver was deleted
	} else if err != nil {
		return err
	}

	at := c.At.UTC().Truncate(time.Second)
	current, err := s.events.At(ctx, stint.DriverID, at)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return err
	}
	if current != nil && current.StartedAt.Equal(at) {
		return nil // something already starts at that second
	}

	var status model.DutyStatus
	switch c.Kind {
	case model.DriveStarted:
		if current != nil && (current.Status == model.DutyDriving || current.Special != nil) {
			return nil
		}
		status = model.DutyDriving
	case model.StopConfirmed:
		if current == nil || current.Status != model.DutyDriving || current.Origin != model.DutyOriginAuto {
			return nil
		}
		status = model.DutyOnDuty
	default:
		return fmt.Errorf("unknown motion change %q", c.Kind)
	}

	p := c.Point
	odo, hours := p.OdometerMi, p.EngineHours
	_, err = s.events.Insert(ctx, model.DutyStatusEvent{
		CompanyID: stint.CompanyID, DriverID: stint.DriverID, Status: status, StartedAt: at,
		Origin: model.DutyOriginAuto, TruckID: &truckID,
		Latitude: p.Latitude, Longitude: p.Longitude, OdometerMi: &odo, EngineHours: &hours,
	})
	return err
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
