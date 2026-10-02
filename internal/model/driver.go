package model

import (
	"time"

	"github.com/google/uuid"
)

// Driver is the driver profile of a user. Login, name and active status live
// on the User; license data lives here.
type Driver struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	CompanyID        uuid.UUID
	LicenseNumber    string
	LicenseState     string    // two-letter US state code, e.g. TX
	LicenseExpiresAt time.Time // a calendar day (DATE); time part is zero
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// DriverDetails is a driver with the user fields clients need to display it.
type DriverDetails struct {
	Driver Driver
	User   User
}

// DriverFilter narrows a driver list. The zero value means "no filter".
type DriverFilter struct {
	CompanyID *uuid.UUID
}
