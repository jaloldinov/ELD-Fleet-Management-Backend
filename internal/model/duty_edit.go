package model

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

// RecordStatus: an edit never deletes a record. The old version stays,
// inactive, so anyone can see what the log said before (FMCSA keeps
// every version).
type RecordStatus string

const (
	RecordActive   RecordStatus = "active"   // part of the log
	RecordInactive RecordStatus = "inactive" // replaced by an edit; history
)

// Edit rules.
const (
	// DutyEditWindow: how far back a driver may fix the log. The current
	// day and the 7 before it are what a driver must be able to show at a
	// roadside inspection; older days are settled.
	DutyEditWindow = 8 * 24 * time.Hour
	// Every edit says why (an FMCSA annotation: 4 to 60 characters).
	MinEditReasonLength = 4
	MaxEditReasonLength = 60
)

// Interval is [Start, End).
type Interval struct {
	Start, End time.Time
}

// DrivingIntervals lists when the driver was driving within [from, to),
// from active records sorted oldest first (one record in force at from
// may start before it). Back-to-back driving records make one interval.
//
// Edits compare it before and after: driving belongs to the ELD, so an
// edit that would shorten, stretch or remove it is refused.
func DrivingIntervals(events []DutyStatusEvent, from, to time.Time) []Interval {
	var out []Interval
	for i, e := range events {
		if e.Status != DutyDriving {
			continue
		}
		start, end := maxTime(e.StartedAt, from), to
		if i+1 < len(events) {
			end = minTime(events[i+1].StartedAt, to)
		}
		if !end.After(start) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End.Equal(start) {
			out[n-1].End = end
			continue
		}
		out = append(out, Interval{start, end})
	}
	return out
}

// SameIntervals reports whether two interval lists are identical.
func SameIntervals(a, b []Interval) bool {
	return slices.EqualFunc(a, b, func(x, y Interval) bool {
		return x.Start.Equal(y.Start) && x.End.Equal(y.End)
	})
}

// LogCertification: the driver signs a day as true and complete ("I hereby
// certify that my data entries and my record of duty status for this
// 24-hour period are true and correct"). Append-only: a day changed after
// it was signed is signed again, with a new row.
type LogCertification struct {
	ID          uuid.UUID
	CompanyID   uuid.UUID
	DriverID    uuid.UUID
	Date        string // YYYY-MM-DD, in the company's time zone
	CertifiedAt time.Time
	CertifiedBy uuid.UUID
}
