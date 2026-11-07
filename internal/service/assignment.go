package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// DriverAssignmentRepository is what AssignmentService needs to store stints.
type DriverAssignmentRepository interface {
	Open(ctx context.Context, a model.DriverAssignment) (*model.DriverAssignment, error)
	GetOpenByDriver(ctx context.Context, driverID uuid.UUID) (*model.DriverAssignment, error)
	EndOpenByDriver(ctx context.Context, driverID uuid.UUID, endedBy *uuid.UUID) (*model.DriverAssignment, error)
	EndOpenByDriverUser(ctx context.Context, userID uuid.UUID, endedBy *uuid.UUID) ([]model.DriverAssignment, error)
	EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) ([]model.DriverAssignment, error)
}

// ELDAssignmentRepository is what AssignmentService needs to store which
// device is in which truck.
type ELDAssignmentRepository interface {
	Open(ctx context.Context, a model.ELDAssignment) (*model.ELDAssignment, error)
	GetOpenByDevice(ctx context.Context, deviceID uuid.UUID) (*model.ELDAssignment, error)
	GetOpenByTruck(ctx context.Context, truckID uuid.UUID) (*model.ELDAssignment, error)
	EndOpenByDevice(ctx context.Context, deviceID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error)
	EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error)
}

// DeviceLocker locks an ELD device's row (repository.ELDDeviceRepository).
type DeviceLocker interface {
	LockByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
}

// AssignmentDrivers finds and locks drivers (repository.DriverRepository).
type AssignmentDrivers interface {
	LockByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
}

// TruckLocker locks a truck's row for the rest of the transaction
// (repository.TruckRepository).
type TruckLocker interface {
	LockByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
}

// AssignDriverInput: Role empty means primary.
type AssignDriverInput struct {
	TruckID uuid.UUID
	Role    model.AssignmentRole
}

// AssignmentService is the dispatch board: which drivers drive which truck,
// and which ELD device is installed in it.
//
// Drivers are put on trucks by the office (dispatchers, managers, admins)
// for any driver of the company, or by a driver for themselves (the *Me
// methods, like picking a truck at login in a real ELD app). Devices are
// installed by managers.
//
// Every write runs in one transaction that first LOCKS the driver (or the
// device), then the truck. Always in that order: if one request locked the
// truck first and another the driver first, each would wait for the other
// forever (a deadlock; Postgres would kill one of them).
type AssignmentService struct {
	assignments DriverAssignmentRepository
	elds        ELDAssignmentRepository
	drivers     AssignmentDrivers
	devices     DeviceLocker
	trucks      TruckLocker
	audit       *Auditor
}

func NewAssignmentService(assignments DriverAssignmentRepository, elds ELDAssignmentRepository,
	drivers AssignmentDrivers, devices DeviceLocker, trucks TruckLocker, audit *Auditor) *AssignmentService {
	return &AssignmentService{assignments: assignments, elds: elds, drivers: drivers, devices: devices, trucks: trucks, audit: audit}
}

// AssignDriver puts the driver on the truck in the given role. A driver
// already on another truck (or in another role) is moved: the old stint
// ends and a new one starts, in the same transaction. Asking for what is
// already true changes nothing and returns the current stint.
func (s *AssignmentService) AssignDriver(ctx context.Context, p auth.Principal, driverID uuid.UUID, in AssignDriverInput) (*model.DriverAssignment, error) {
	return s.assign(ctx, p, driverID, in)
}

// AssignMe is AssignDriver for the caller's own driver profile.
// ErrNotFound if the caller is not a driver.
func (s *AssignmentService) AssignMe(ctx context.Context, p auth.Principal, in AssignDriverInput) (*model.DriverAssignment, error) {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return s.assign(ctx, p, dd.Driver.ID, in)
}

// UnassignDriver takes the driver off their truck. A driver on no truck is
// not an error: the result is the same.
func (s *AssignmentService) UnassignDriver(ctx context.Context, p auth.Principal, driverID uuid.UUID) error {
	return s.unassign(ctx, p, driverID)
}

// UnassignMe is UnassignDriver for the caller's own driver profile.
func (s *AssignmentService) UnassignMe(ctx context.Context, p auth.Principal) error {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return err
	}
	return s.unassign(ctx, p, dd.Driver.ID)
}

func (s *AssignmentService) assign(ctx context.Context, p auth.Principal, driverID uuid.UUID, in AssignDriverInput) (*model.DriverAssignment, error) {
	role := in.Role
	if role == "" {
		role = model.AssignmentPrimary
	}
	if !role.Valid() {
		return nil, &validation.ValidationError{Fields: map[string]string{"role": "must be primary or co_driver"}}
	}

	var result *model.DriverAssignment
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		// 1. Lock the driver: a concurrent delete waits for us.
		dd, err := s.lockDriverInScope(ctx, p, driverID)
		if err != nil {
			return err
		}
		if !dd.User.IsActive {
			return &validation.ValidationError{Fields: map[string]string{"driver": "the driver's account is disabled"}}
		}

		// 2. Lock the truck: a concurrent deactivation or delete waits for
		// us, so "active" stays true until we COMMIT.
		t, err := s.trucks.LockByID(ctx, in.TruckID)
		// Another company's truck gets the same answer as a missing one, so
		// truck IDs of other tenants cannot be probed. Also checked by the
		// composite foreign key in the database.
		if errors.Is(err, model.ErrNotFound) || (err == nil && t.CompanyID != dd.Driver.CompanyID) {
			return &validation.ValidationError{Fields: map[string]string{"truck_id": "truck not found"}}
		} else if err != nil {
			return err
		}
		if !t.IsActive {
			return &validation.ValidationError{Fields: map[string]string{"truck_id": "truck is out of service"}}
		}

		// 3. Already there? Then nothing to do (PUT is idempotent).
		current, err := s.assignments.GetOpenByDriver(ctx, driverID)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return err
		}
		if current != nil && current.TruckID == t.ID && current.Role == role {
			result = current
			return nil
		}

		// 4. Moving: end the old stint first, or the new one would break
		// "one open stint per driver". ErrNotFound: the old truck was just
		// deleted or deactivated and its stints ended while we waited for
		// the row. Nothing left to end, so carry on.
		if current != nil {
			_, err := s.assignments.EndOpenByDriver(ctx, driverID, actorID(p))
			if err != nil && !errors.Is(err, model.ErrNotFound) {
				return err
			}
		}
		result, err = s.assignments.Open(ctx, model.DriverAssignment{
			CompanyID: dd.Driver.CompanyID, DriverID: driverID, TruckID: t.ID, Role: role, StartedBy: actorID(p),
		})
		if err != nil {
			return err // ErrSeatTaken: someone else holds that seat
		}
		return s.record(ctx, p, "driver.assign_truck", dd.Driver, assignmentChanges(current, result))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *AssignmentService) unassign(ctx context.Context, p auth.Principal, driverID uuid.UUID) error {
	return s.audit.WithinTx(ctx, func(ctx context.Context) error {
		dd, err := s.lockDriverInScope(ctx, p, driverID)
		if err != nil {
			return err
		}
		ended, err := s.assignments.EndOpenByDriver(ctx, driverID, actorID(p))
		if errors.Is(err, model.ErrNotFound) {
			return nil // on no truck: nothing to end, nothing to record
		} else if err != nil {
			return err
		}
		return s.record(ctx, p, "driver.unassign_truck", dd.Driver, assignmentChanges(ended, nil))
	})
}

// ReleaseTruck ends every stint on a truck that leaves service (deleted or
// deactivated), and records each on its driver. It runs in the CALLER's
// transaction (ctx must carry it), which must have LOCKED the truck first:
// an assignment in progress then finishes before we look, so its stint is
// ended too, and a later one waits and sees the truck gone.
//
// Why not one SQL statement (a CTE) inside the truck's UPDATE? A statement
// sees the data as it was when it STARTED. If it waits for the truck's lock
// while an assignment commits, the CTE never sees that new stint, and it
// stays open on a deleted truck. Separate statements after the lock do.
//
// The truck's ELD device is taken out too: it is ready for another truck.
func (s *AssignmentService) ReleaseTruck(ctx context.Context, p auth.Principal, truckID uuid.UUID) error {
	ended, err := s.assignments.EndOpenByTruck(ctx, truckID, actorID(p))
	if err != nil {
		return err
	}
	if err := s.recordReleased(ctx, p, ended); err != nil {
		return err
	}
	device, err := s.elds.EndOpenByTruck(ctx, truckID, actorID(p))
	if errors.Is(err, model.ErrNotFound) {
		return nil // no device in it
	} else if err != nil {
		return err
	}
	return s.recordDevice(ctx, p, "eld_device.unassign_truck", device.ELDDeviceID, device.CompanyID, &device.TruckID, nil)
}

// ReleaseUser ends the stint of the driver behind a user account that is
// being deactivated or deleted (no stint, or not a driver: nothing to do).
// It runs in the CALLER's transaction, AFTER the caller updated the users
// row: that UPDATE waited for any assignment holding the account (see
// DriverRepository.LockByID), so its stint is visible and ended here.
func (s *AssignmentService) ReleaseUser(ctx context.Context, p auth.Principal, userID uuid.UUID) error {
	ended, err := s.assignments.EndOpenByDriverUser(ctx, userID, actorID(p))
	if err != nil {
		return err
	}
	return s.recordReleased(ctx, p, ended)
}

// ---------- ELD devices ----------

// InstallDevice installs the device in a truck of the same company, or moves
// it there from its current truck. If the truck already has another device:
// ErrTruckHasELD, unless replace is true, in which case that device is taken
// out first (e.g. it broke and this one replaces it). Installing a device
// where it already is changes nothing.
func (s *AssignmentService) InstallDevice(ctx context.Context, p auth.Principal, deviceID, truckID uuid.UUID, replace bool) (*model.ELDDevice, error) {
	var result *model.ELDDevice
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		// 1. The device, locked: a concurrent delete or second install waits.
		d, err := s.lockDeviceInScope(ctx, p, deviceID)
		if err != nil {
			return err
		}
		if !d.IsActive {
			return &validation.ValidationError{Fields: map[string]string{"is_active": "an inactive device cannot be installed"}}
		}

		// 2. The truck, locked: it cannot be deleted or deactivated before
		// we commit (this was a race before Phase 3: check, then write).
		t, err := s.trucks.LockByID(ctx, truckID)
		if errors.Is(err, model.ErrNotFound) || (err == nil && t.CompanyID != d.CompanyID) {
			return &validation.ValidationError{Fields: map[string]string{"truck_id": "truck not found"}}
		} else if err != nil {
			return err
		}
		if !t.IsActive {
			return &validation.ValidationError{Fields: map[string]string{"truck_id": "truck is out of service"}}
		}

		// 3. Already there? Nothing to do.
		current, err := s.elds.GetOpenByDevice(ctx, deviceID)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return err
		}
		if current != nil && current.TruckID == truckID {
			d.TruckID = &truckID
			result = d
			return nil
		}

		// 4. The truck's current device: refuse, or take it out (replace).
		if replace {
			old, err := s.elds.EndOpenByTruck(ctx, truckID, actorID(p))
			if err != nil && !errors.Is(err, model.ErrNotFound) {
				return err
			}
			if old != nil {
				err := s.recordDevice(ctx, p, "eld_device.unassign_truck", old.ELDDeviceID, old.CompanyID, &old.TruckID, nil)
				if err != nil {
					return err
				}
			}
		}

		// 5. Moving from another truck: end that stint first.
		if current != nil {
			if _, err := s.elds.EndOpenByDevice(ctx, deviceID, actorID(p)); err != nil && !errors.Is(err, model.ErrNotFound) {
				return err
			}
		}
		if _, err := s.elds.Open(ctx, model.ELDAssignment{
			CompanyID: d.CompanyID, ELDDeviceID: deviceID, TruckID: truckID, StartedBy: actorID(p),
		}); err != nil {
			return err // ErrTruckHasELD: the truck has a device and replace was false
		}

		var oldTruck *uuid.UUID
		if current != nil {
			oldTruck = &current.TruckID
		}
		d.TruckID = &truckID
		result = d
		return s.recordDevice(ctx, p, "eld_device.assign_truck", deviceID, d.CompanyID, oldTruck, &truckID)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RemoveDevice takes the device out of its truck. A device that is not
// installed is not an error: the result is the same.
func (s *AssignmentService) RemoveDevice(ctx context.Context, p auth.Principal, deviceID uuid.UUID) (*model.ELDDevice, error) {
	var result *model.ELDDevice
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		d, err := s.lockDeviceInScope(ctx, p, deviceID)
		if err != nil {
			return err
		}
		d.TruckID = nil
		result = d
		return s.releaseDevice(ctx, p, deviceID)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReleaseDevice takes a device that is being deleted out of its truck. It
// runs in the CALLER's transaction, which must have locked the device.
func (s *AssignmentService) ReleaseDevice(ctx context.Context, p auth.Principal, deviceID uuid.UUID) error {
	return s.releaseDevice(ctx, p, deviceID)
}

func (s *AssignmentService) releaseDevice(ctx context.Context, p auth.Principal, deviceID uuid.UUID) error {
	ended, err := s.elds.EndOpenByDevice(ctx, deviceID, actorID(p))
	if errors.Is(err, model.ErrNotFound) {
		return nil // not installed: nothing to end, nothing to record
	} else if err != nil {
		return err
	}
	return s.recordDevice(ctx, p, "eld_device.unassign_truck", deviceID, ended.CompanyID, &ended.TruckID, nil)
}

func (s *AssignmentService) lockDeviceInScope(ctx context.Context, p auth.Principal, deviceID uuid.UUID) (*model.ELDDevice, error) {
	d, err := s.devices.LockByID(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && d.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return d, nil
}

// recordDevice audits a device going from truck old to truck new (nil = no
// truck), with the same actions and fields as before Phase 3.
func (s *AssignmentService) recordDevice(ctx context.Context, p auth.Principal, action string, deviceID, companyID uuid.UUID, old, new *uuid.UUID) error {
	c := model.AuditChanges{}
	c.Set("truck_id", old, new)
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditELDDevice, EntityID: deviceID.String(), CompanyID: &companyID, Changes: c,
	})
}

// recordReleased audits each ended stint on its driver, like an unassign.
func (s *AssignmentService) recordReleased(ctx context.Context, p auth.Principal, ended []model.DriverAssignment) error {
	for i := range ended {
		a := &ended[i]
		d := model.Driver{ID: a.DriverID, CompanyID: a.CompanyID}
		if err := s.record(ctx, p, "driver.unassign_truck", d, assignmentChanges(a, nil)); err != nil {
			return err
		}
	}
	return nil
}

// lockDriverInScope locks the driver; drivers of other companies "do not
// exist" for company users.
func (s *AssignmentService) lockDriverInScope(ctx context.Context, p auth.Principal, driverID uuid.UUID) (*model.DriverDetails, error) {
	dd, err := s.drivers.LockByID(ctx, driverID)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && dd.Driver.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return dd, nil
}

// The change is recorded on the DRIVER: "driver X moved from truck A to
// truck B" is how people search the history.
func (s *AssignmentService) record(ctx context.Context, p auth.Principal, action string, d model.Driver, ch model.AuditChanges) error {
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditDriver, EntityID: d.ID.String(), CompanyID: &d.CompanyID, Changes: ch,
	})
}

// assignmentChanges describes going from stint old to stint new (either may
// be nil: not on a truck).
func assignmentChanges(old, new *model.DriverAssignment) model.AuditChanges {
	var oldTruck, newTruck *uuid.UUID
	var oldRole, newRole *model.AssignmentRole
	if old != nil {
		oldTruck, oldRole = &old.TruckID, &old.Role
	}
	if new != nil {
		newTruck, newRole = &new.TruckID, &new.Role
	}
	c := model.AuditChanges{}
	c.Set("truck_id", oldTruck, newTruck)
	c.Set("role", oldRole, newRole)
	return c
}

// actorID is who did it, for started_by / ended_by: nil for the system.
func actorID(p auth.Principal) *uuid.UUID {
	if p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}
