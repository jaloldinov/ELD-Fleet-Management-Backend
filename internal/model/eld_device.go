package model

import (
	"time"

	"github.com/google/uuid"
)

// ELDStatus is where a device is in its working life.
type ELDStatus string

const (
	ELDActive  ELDStatus = "active"  // works: may be installed
	ELDFaulty  ELDStatus = "faulty"  // broken: may stay in its truck until replaced
	ELDRetired ELDStatus = "retired" // end of life: not in any truck
)

func (s ELDStatus) Valid() bool {
	return s == ELDActive || s == ELDFaulty || s == ELDRetired
}

// ELDDevice is the hardware installed in a truck that records driving time.
type ELDDevice struct {
	ID              uuid.UUID
	CompanyID       uuid.UUID
	SerialNumber    string // immutable after creation
	Model           string
	FirmwareVersion string
	TruckID         *uuid.UUID // read from the open ELD stint; nil = not installed
	Status          ELDStatus
	IsActive        bool // read-only, computed by the database: Status == active
	// When the device's API key was issued (nil = no key: the device cannot
	// call the API). The key itself is never kept.
	KeyCreatedAt *time.Time
	// The device's last authenticated request (to the minute): data or a
	// heartbeat. Online = seen within DeviceOfflineAfter.
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// ELDDeviceFilter narrows a device list. The zero value means "no filter".
type ELDDeviceFilter struct {
	CompanyID *uuid.UUID
	IsActive  *bool
	Status    *ELDStatus
	TruckID   *uuid.UUID
}
