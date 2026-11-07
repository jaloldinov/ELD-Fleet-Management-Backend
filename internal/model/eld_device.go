package model

import (
	"time"

	"github.com/google/uuid"
)

// ELDDevice is the hardware installed in a truck that records driving time.
type ELDDevice struct {
	ID              uuid.UUID
	CompanyID       uuid.UUID
	SerialNumber    string // immutable after creation
	Model           string
	FirmwareVersion string
	TruckID         *uuid.UUID // read from the open ELD stint; nil = not installed
	IsActive        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// ELDDeviceFilter narrows a device list. The zero value means "no filter".
type ELDDeviceFilter struct {
	CompanyID *uuid.UUID
	IsActive  *bool
	TruckID   *uuid.UUID
}
