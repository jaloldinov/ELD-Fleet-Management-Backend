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
