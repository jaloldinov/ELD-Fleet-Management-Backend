package model

import (
	"time"

	"github.com/google/uuid"
)

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
	IsActive     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// TruckFilter narrows a truck list. The zero value means "no filter".
type TruckFilter struct {
	CompanyID *uuid.UUID
	IsActive  *bool
}
