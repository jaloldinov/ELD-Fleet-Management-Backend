package model

import "time"

// AutoOnDutyAfterStop: a truck that stood still this long ends the
// driver's automatic driving status; they become on duty (not driving).
// FMCSA (49 CFR 395 App. A, 4.3.1.3) prompts the driver after 5 minutes
// stopped and switches if they do not answer within a minute; we have no
// prompt, so we switch at 5 minutes.
const AutoOnDutyAfterStop = 5 * time.Minute

// Motion is a device's current run: moving or standing since Since, last
// heard from at Last (its newest reading).
type Motion struct {
	Moving bool
	Since  time.Time // the first reading of this run
	Last   time.Time // the newest reading
}

// MotionChangeKind is what a truck's movement means for its driver.
type MotionChangeKind string

const (
	// DriveStarted: the truck began to move. The driver is driving.
	DriveStarted MotionChangeKind = "drive_started"
	// StopConfirmed: the truck has stood still for AutoOnDutyAfterStop.
	// A driver still driving becomes on duty.
	StopConfirmed MotionChangeKind = "stop_confirmed"
)

// MotionChange happened At; Point is the reading that tells us (for the
// record's position, odometer and engine hours).
type MotionChange struct {
	Kind  MotionChangeKind
	At    time.Time
	Point TelemetryPoint
}

// MotionOf is the run a stored state is in; nil = no reading yet.
func MotionOf(st *DeviceState) *Motion {
	if st == nil {
		return nil
	}
	return &Motion{Moving: st.Moving, Since: st.MotionSince, Last: st.RecordedAt}
}

// Step feeds the next reading (newer than m.Last) into the run and says
// what it means. m nil = this is the device's first reading.
//
//	standing ... moving       -> DriveStarted at the first moving reading
//	moving ... standing 5 min -> StopConfirmed at stop + 5 min
//
// A stop is confirmed ONCE: by the reading that crosses the 5-minute mark,
// or, if the truck drives off before any reading came after the mark (the
// device was quiet), by the moving reading that ends the stop. A device
// that goes quiet for good (engine off) is handled by Silence.
func (m *Motion) Step(p TelemetryPoint) (Motion, []MotionChange) {
	moving := p.Moving()
	if m == nil {
		next := Motion{Moving: moving, Since: p.RecordedAt, Last: p.RecordedAt}
		if moving {
			return next, []MotionChange{{Kind: DriveStarted, At: p.RecordedAt, Point: p}}
		}
		return next, nil
	}

	var changes []MotionChange
	if !m.Moving {
		if due := m.Since.Add(AutoOnDutyAfterStop); m.Last.Before(due) && !p.RecordedAt.Before(due) {
			changes = append(changes, MotionChange{Kind: StopConfirmed, At: due, Point: p})
		}
	}
	next := Motion{Moving: m.Moving, Since: m.Since, Last: p.RecordedAt}
	if moving != m.Moving {
		next.Moving, next.Since = moving, p.RecordedAt
		if moving {
			changes = append(changes, MotionChange{Kind: DriveStarted, At: p.RecordedAt, Point: p})
		}
	}
	return next, changes
}

// Silence: the device sent nothing new, and it is now `now`. A truck
// standing since before now - 5 min, with no reading after the mark yet,
// has a stop to confirm (ok = true). Readings after the mark confirmed it
// already (Step).
func (m Motion) Silence(now time.Time) (time.Time, bool) {
	if m.Moving {
		return time.Time{}, false
	}
	due := m.Since.Add(AutoOnDutyAfterStop)
	return due, m.Last.Before(due) && !now.Before(due)
}
