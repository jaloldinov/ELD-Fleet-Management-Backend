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

// Time rules for readings.
const (
	// MaxClockSkew: FMCSA's timing rule (49 CFR 395 App. A, 4.3.1.5): an
	// ELD's clock must stay within 10 minutes of UTC, or the device is in a
	// timing malfunction. A reading further in the future than that cannot
	// be trusted.
	MaxClockSkew = 10 * time.Minute
	// MaxReadingAge: older readings are refused (a clock reset to 2000 after
	// a power loss, or a device offline for over a month). Generous on
	// purpose: refusing a real reading loses a legal record.
	MaxReadingAge = 30 * 24 * time.Hour
)

// DeviceOfflineAfter: a device not heard from (data or heartbeat) for
// longer than this is offline. Devices send at least every minute while
// on, so five quiet minutes means something is wrong (or it is off).
const DeviceOfflineAfter = 5 * time.Minute

// Connection says whether a device is talking to us.
type Connection string

const (
	ConnectionOnline  Connection = "online"
	ConnectionOffline Connection = "offline"
	ConnectionNever   Connection = "never" // has never called the API
)

// ConnectionOf is computed when read, from the last contact: no background
// job has to flip devices to "offline", time does it.
func ConnectionOf(lastSeen *time.Time, now time.Time) Connection {
	switch {
	case lastSeen == nil:
		return ConnectionNever
	case now.Sub(*lastSeen) <= DeviceOfflineAfter:
		return ConnectionOnline
	default:
		return ConnectionOffline
	}
}

// DeviceState is a device's last known reading: the newest by recorded_at,
// not the last to arrive.
type DeviceState struct {
	TelemetryPoint
	ELDDeviceID uuid.UUID
	CompanyID   uuid.UUID
	TruckID     *uuid.UUID // the truck it was in at RecordedAt
	ReceivedAt  time.Time
	Moving      bool
	// MotionSince: the first reading of the current run (moving or
	// standing). See Motion.
	MotionSince time.Time
}
