package model

import (
	"time"

	"github.com/google/uuid"
)

// MovingSpeedMPH: above this speed the truck is moving. 5 mph is the FMCSA
// threshold for switching a driver to "driving" automatically (Phase 5).
const MovingSpeedMPH = 5.0

// TelemetryPoint is one reading an ELD device reports.
type TelemetryPoint struct {
	Seq         int64     // the device's counter: (device, seq) is unique
	RecordedAt  time.Time // the device's clock
	Latitude    *float64  // nil (with Longitude) = no GPS fix
	Longitude   *float64
	SpeedMPH    float64
	OdometerMi  float64
	EngineHours float64
	EngineOn    bool
}

// Moving is decided by us, from speed, not reported by the device.
func (p TelemetryPoint) Moving() bool {
	return p.SpeedMPH > MovingSpeedMPH
}

// ELDEvent is a stored TelemetryPoint.
type ELDEvent struct {
	TelemetryPoint
	ID          uuid.UUID
	CompanyID   uuid.UUID
	ELDDeviceID uuid.UUID
	TruckID     *uuid.UUID // the truck the device was in at RecordedAt; nil = none
	ReceivedAt  time.Time
	Moving      bool
}
