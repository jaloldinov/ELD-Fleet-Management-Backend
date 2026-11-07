package model

import (
	"time"

	"github.com/google/uuid"
)

// TruckStatus is where a truck is in its working life.
type TruckStatus string

const (
	TruckActive        TruckStatus = "active"         // on the road: drivers may be assigned
	TruckInMaintenance TruckStatus = "in_maintenance" // in the shop: no drivers, keeps its ELD
	TruckOutOfService  TruckStatus = "out_of_service" // left the fleet
)

func (s TruckStatus) Valid() bool {
	return s == TruckActive || s == TruckInMaintenance || s == TruckOutOfService
}

// Truck is a power unit operated by a carrier.
type Truck struct {
	ID           uuid.UUID
	CompanyID    uuid.UUID
	UnitNumber   string // the carrier's own number, e.g. "101"
	VIN          string // immutable after creation
	Make         string
	Model        string
	Year         *int    // optional
	LicensePlate *string // set together with PlateState, or both nil
	PlateState   *string
	Status       TruckStatus
	// IsActive is read-only, computed by the database from Status: "part of
	// the fleet" (not out of service). Per-truck billing counts these.
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// TruckFilter narrows a truck list. The zero value means "no filter".
type TruckFilter struct {
	CompanyID *uuid.UUID
	IsActive  *bool
	Status    *TruckStatus
}
