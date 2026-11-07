package model

import (
	"time"

	"github.com/google/uuid"
)

// AssignmentRole is the driver's place in the truck. Not to be confused with
// RBAC roles (RoleDriver, ...): this one is about the cab, not permissions.
type AssignmentRole string

const (
	AssignmentPrimary  AssignmentRole = "primary"
	AssignmentCoDriver AssignmentRole = "co_driver" // team driving
)

func (r AssignmentRole) Valid() bool {
	return r == AssignmentPrimary || r == AssignmentCoDriver
}

// DriverAssignment is one stint of a driver on a truck. EndedAt nil means the
// driver is on that truck now; otherwise it is history.
type DriverAssignment struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
	DriverID  uuid.UUID
	TruckID   uuid.UUID
	Role      AssignmentRole
	StartedAt time.Time
	EndedAt   *time.Time
	StartedBy *uuid.UUID // nil = done by the system
	EndedBy   *uuid.UUID
}

func (a DriverAssignment) IsCurrent() bool {
	return a.EndedAt == nil
}

// ELDAssignment is one stint of an ELD device installed in a truck. EndedAt
// nil means it is installed there now.
type ELDAssignment struct {
	ID          uuid.UUID
	CompanyID   uuid.UUID
	ELDDeviceID uuid.UUID
	TruckID     uuid.UUID
	StartedAt   time.Time
	EndedAt     *time.Time
	StartedBy   *uuid.UUID // nil = done by the system
	EndedBy     *uuid.UUID
}

// CrewMember is a driver on a truck now, with the name people know them by.
type CrewMember struct {
	Assignment DriverAssignment
	FirstName  string
	LastName   string
}

// InstalledDevice is the ELD device in a truck now.
type InstalledDevice struct {
	Assignment   ELDAssignment
	SerialNumber string
}

// TruckBoard is a truck's line on the dispatch board: who drives it now
// (primary first, at most two) and which device is installed (nil = none).
type TruckBoard struct {
	Truck  Truck
	Crew   []CrewMember
	Device *InstalledDevice
}

// DriverTruck answers "where is this driver now?". Assignment and Board are
// nil when the driver is on no truck.
type DriverTruck struct {
	DriverID   uuid.UUID
	Assignment *DriverAssignment
	Board      *TruckBoard
}

// StintPeriod filters stints by time. At: stints running at that instant
// ("who drove truck 101 at 14:30?"). From/To: stints that overlap
// [From, To), even partly. At and From/To are not combined.
type StintPeriod struct {
	At       *time.Time
	From, To *time.Time
}

// DriverAssignmentFilter narrows the driver stint history. The zero value
// means "no filter".
type DriverAssignmentFilter struct {
	CompanyID *uuid.UUID
	DriverID  *uuid.UUID
	TruckID   *uuid.UUID
	Period    StintPeriod
}

// DriverAssignmentRecord is a stint with the names people know: the truck's
// unit number and the driver's name. Deleted trucks and drivers keep theirs
// here: history outlives them.
type DriverAssignmentRecord struct {
	Assignment      DriverAssignment
	TruckUnitNumber string
	DriverFirstName string
	DriverLastName  string
}

// ELDAssignmentFilter narrows the device stint history.
type ELDAssignmentFilter struct {
	CompanyID   *uuid.UUID
	ELDDeviceID *uuid.UUID
	TruckID     *uuid.UUID
	Period      StintPeriod
}

// ELDAssignmentRecord is a device stint with the device's serial number and
// the truck's unit number.
type ELDAssignmentRecord struct {
	Assignment      ELDAssignment
	TruckUnitNumber string
	SerialNumber    string
}
