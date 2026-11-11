package model

import (
	"time"

	"github.com/google/uuid"
)

// DutySegment is a piece of the day in one duty status: the record in
// force, cut to the day. A record that began yesterday at 20:00 starts
// today's segment at 00:00.
type DutySegment struct {
	Event      DutyStatusEvent
	Start, End time.Time
}

func (s DutySegment) Duration() time.Duration { return s.End.Sub(s.Start) }

// DailyLog is one driver's day: the paper log's 24-hour grid, as data.
//
// The day runs from midnight to midnight in the company's home-terminal
// time zone, wherever the truck is. It is 24 hours, except on the two
// daylight-saving days (23 h in March, 25 h in November).
type DailyLog struct {
	DriverID uuid.UUID
	Date     string // YYYY-MM-DD, in Location
	Location *time.Location
	Start    time.Time // local midnight
	End      time.Time // the next local midnight
	// Until: the log is known up to here. End for a past day; now for today
	// (the rest of today has not happened yet).
	Until    time.Time
	Segments []DutySegment // oldest first, no gaps, no overlaps
	// Totals per status line of the grid. Personal conveyance is drawn on
	// the off-duty line, yard moves on the on-duty line: their status says so.
	Totals map[DutyStatus]time.Duration
	// Unrecorded: the part of the day before the driver's first record ever
	// (a new driver). Totals + Unrecorded always equal Until - Start.
	Unrecorded time.Duration

	// History: the day's records that edits replaced (inactive), oldest first.
	History []DutyStatusEvent
	// LastChangeAt: when the day's log last changed (a record written or
	// replaced). Zero for a day with no records.
	LastChangeAt time.Time
	// CertifiedAt: the driver's latest signature of the day; nil = never.
	CertifiedAt *time.Time
}

// Certified: signed, and not changed since. A day edited (or recorded on)
// after its signature needs signing again.
func (l DailyLog) Certified() bool {
	return l.CertifiedAt != nil && !l.CertifiedAt.Before(l.LastChangeAt)
}

// SetHistory adds the replaced records and works out LastChangeAt from
// them and the records in the day's segments.
func (l *DailyLog) SetHistory(inactive []DutyStatusEvent) {
	l.History = inactive
	for _, sg := range l.Segments {
		l.LastChangeAt = maxTime(l.LastChangeAt, sg.Event.CreatedAt)
	}
	for _, e := range inactive {
		if e.InactivatedAt != nil {
			l.LastChangeAt = maxTime(l.LastChangeAt, *e.InactivatedAt)
		}
	}
}

// DayBounds returns the local midnight that starts date (YYYY-MM-DD) in loc
// and the next one.
func DayBounds(date string, loc *time.Location) (start, end time.Time, err error) {
	start, err = time.ParseInLocation(time.DateOnly, date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	// AddDate, not Add(24h): the next midnight on the calendar, so the
	// daylight-saving days come out 23 and 25 hours long.
	return start, start.AddDate(0, 0, 1), nil
}

// BuildDailyLog cuts a driver's records into the day [start, end), up to
// now. events are the records covering the day, oldest first, including
// the one already in force at start (DutyStatusRepository.ListPeriod).
func BuildDailyLog(events []DutyStatusEvent, start, end, now time.Time) DailyLog {
	log := DailyLog{Start: start, End: end, Until: end, Totals: map[DutyStatus]time.Duration{
		DutyOffDuty: 0, DutySleeperBerth: 0, DutyDriving: 0, DutyOnDuty: 0,
	}}
	if now.Before(end) {
		log.Until = now
	}
	if !log.Until.After(start) {
		log.Until = start // a day that has not begun: empty
		return log
	}

	for i, e := range events {
		from := maxTime(e.StartedAt, start)
		to := log.Until
		if i+1 < len(events) {
			to = minTime(events[i+1].StartedAt, log.Until)
		}
		if !to.After(from) {
			continue // ends before the day, or starts after Until
		}
		log.Segments = append(log.Segments, DutySegment{Event: e, Start: from, End: to})
		log.Totals[e.Status] += to.Sub(from)
	}

	log.Unrecorded = log.Until.Sub(start)
	if len(log.Segments) > 0 {
		log.Unrecorded = log.Segments[0].Start.Sub(start)
	}
	return log
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
