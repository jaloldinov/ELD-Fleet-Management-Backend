package model

import (
	"time"

	"github.com/google/uuid"
)

// DutyStatus is what a driver is doing. At any moment a driver is in
// exactly one of these four.
type DutyStatus string

const (
	DutyOffDuty      DutyStatus = "off_duty"      // not working: home, meal, rest
	DutySleeperBerth DutyStatus = "sleeper_berth" // resting in the cab's bunk
	DutyDriving      DutyStatus = "driving"       // at the wheel, truck moving
	DutyOnDuty       DutyStatus = "on_duty"       // working, not driving: loading, fueling, inspection
)

func (s DutyStatus) Valid() bool {
	switch s {
	case DutyOffDuty, DutySleeperBerth, DutyDriving, DutyOnDuty:
		return true
	}
	return false
}

// SpecialDriving marks truck movement that is not counted as driving.
type SpecialDriving string

const (
	// SpecialPersonalConveyance: using the truck for yourself (to a
	// restaurant, to the motel). Off duty.
	SpecialPersonalConveyance SpecialDriving = "personal_conveyance"
	// SpecialYardMoves: moving the truck inside a yard or terminal. On duty.
	SpecialYardMoves SpecialDriving = "yard_moves"
)

// AllowedWith reports whether the category may go with a status: personal
// conveyance only off duty, yard moves only on duty.
func (s SpecialDriving) AllowedWith(status DutyStatus) bool {
	switch s {
	case SpecialPersonalConveyance:
		return status == DutyOffDuty
	case SpecialYardMoves:
		return status == DutyOnDuty
	}
	return false
}

// DutyOrigin says who wrote a record.
type DutyOrigin string

const (
	DutyOriginAuto   DutyOrigin = "auto"   // the ELD (driving from truck movement)
	DutyOriginDriver DutyOrigin = "driver" // the driver pressed a button
)

// DutyStatusEvent is one change of a driver's duty status. It records only
// when the status STARTED; it lasts until the driver's next event starts.
type DutyStatusEvent struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
	DriverID  uuid.UUID
	Status    DutyStatus
	Special   *SpecialDriving // nil = none
	StartedAt time.Time
	Origin    DutyOrigin

	// Where it happened, from the truck's ELD at StartedAt. All nil when
	// the driver was in no truck or the truck had no reading.
	TruckID     *uuid.UUID
	Latitude    *float64
	Longitude   *float64
	OdometerMi  *float64
	EngineHours *float64

	Note      *string
	CreatedBy *uuid.UUID // the user who entered it; nil for auto events
	CreatedAt time.Time
}
