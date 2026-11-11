package model

import "time"

// Hours of service: the FMCSA limits for property-carrying drivers
// (49 CFR 395.3), the basic rules only. Not modelled: the split sleeper
// berth, adverse driving conditions, short-haul and other exceptions.
const (
	// MaxDrivingPerShift: at most 11 hours of driving per shift.
	MaxDrivingPerShift = 11 * time.Hour
	// DutyWindow: no driving after the 14th hour since the shift began.
	// Breaks do not stop this clock.
	DutyWindow = 14 * time.Hour
	// BreakAfterDriving: after 8 hours of driving, a 30-minute break.
	BreakAfterDriving = 8 * time.Hour
	// BreakLength: 30 consecutive minutes not driving (off duty, sleeper
	// or on duty all count).
	BreakLength = 30 * time.Minute
	// ShiftRest: 10 consecutive hours off duty or in the sleeper start a
	// new shift: the 11- and 14-hour clocks are full again.
	ShiftRest = 10 * time.Hour
	// CycleRestart: 34 consecutive hours off empty the 60/70-hour cycle.
	CycleRestart = 34 * time.Hour
)

// HOSCycle is the carrier's weekly limit: on duty (driving or not) in a
// rolling run of days.
type HOSCycle string

const (
	HOSCycle70in8 HOSCycle = "us_70_8" // 70 hours in 8 days (the default)
	HOSCycle60in7 HOSCycle = "us_60_7" // 60 hours in 7 days
)

func (c HOSCycle) Valid() bool { return c == HOSCycle70in8 || c == HOSCycle60in7 }

// Limit and Days of the cycle.
func (c HOSCycle) Limit() (time.Duration, int) {
	if c == HOSCycle60in7 {
		return 60 * time.Hour, 7
	}
	return 70 * time.Hour, 8
}

// HOSLookback: how much of the log the engine reads. The cycle's 8 days,
// and two more to find where the shift and the cycle began. A driver with
// no 10-hour rest in that long is in violation many times over anyway.
const HOSLookback = 10 * 24 * time.Hour

// HOSClocks is where a driver stands at an instant: what is used, what is
// left. "Left" is never negative.
type HOSClocks struct {
	At    time.Time
	Cycle HOSCycle

	// The shift: from the first on-duty moment after a 10-hour rest. Not
	// in a shift (rested 10 hours, or never worked) = every clock is full.
	InShift    bool
	ShiftStart *time.Time

	DrivingUsed time.Duration // in this shift
	DrivingLeft time.Duration // of the 11 hours

	WindowEnd  *time.Time    // shift start + 14 h; nil when not in a shift
	WindowLeft time.Duration // until WindowEnd

	DrivingSinceBreak time.Duration // since the last 30-minute break
	BreakDueIn        time.Duration // driving left before the break is needed

	CycleUsed  time.Duration // on duty in the rolling days (or since a restart)
	CycleLeft  time.Duration
	CycleStart time.Time // where the counting begins: the first rolling day, or a restart's end if later

	// RestStreak: the current run off duty / in the sleeper (0 while
	// working). 10 h of it start a new shift, 34 h restart the cycle.
	RestStreak time.Duration

	// CanDriveFor: how long the driver may drive from At, the tightest of
	// the four limits.
	CanDriveFor time.Duration
}

// dutyKind is what a record means for the clocks.
type dutyKind int

const (
	kindRest    dutyKind = iota // off duty (incl. personal conveyance), sleeper berth
	kindOnDuty                  // on duty not driving (incl. yard moves)
	kindDriving                 // driving
)

func kindOf(s DutyStatus) dutyKind {
	switch s {
	case DutyDriving:
		return kindDriving
	case DutyOnDuty:
		return kindOnDuty
	default:
		return kindRest
	}
}

// hosWalker goes through the log once, oldest first, keeping the clocks.
type hosWalker struct {
	shiftStart       *time.Time
	shiftDriving     time.Duration
	sinceBreak       time.Duration
	notDrivingStreak time.Duration // consecutive non-driving: the 30-minute break
	restStreak       time.Duration // consecutive rest: 10 h and 34 h
	cycleResetAt     time.Time     // the end of the latest 34-hour rest
	onDuty           []Interval    // driving and on duty, for the cycle
}

func (w *hosWalker) step(k dutyKind, a, b time.Time) {
	d := b.Sub(a)
	if k == kindRest {
		w.restStreak += d
		w.notDrivingStreak += d
		if w.restStreak >= ShiftRest {
			w.shiftStart, w.shiftDriving = nil, 0
		}
		if w.notDrivingStreak >= BreakLength {
			w.sinceBreak = 0
		}
		if w.restStreak >= CycleRestart {
			w.cycleResetAt = b
		}
		return
	}

	w.restStreak = 0
	if w.shiftStart == nil {
		start := a
		w.shiftStart = &start
	}
	w.onDuty = append(w.onDuty, Interval{a, b})
	if k == kindOnDuty {
		w.notDrivingStreak += d
		if w.notDrivingStreak >= BreakLength {
			w.sinceBreak = 0
		}
		return
	}
	w.notDrivingStreak = 0
	w.shiftDriving += d
	w.sinceBreak += d
}

// cycleUsed sums on-duty time in [from, to).
func (w *hosWalker) cycleUsed(from, to time.Time) time.Duration {
	var used time.Duration
	for _, iv := range w.onDuty {
		a, b := maxTime(iv.Start, from), minTime(iv.End, to)
		if b.After(a) {
			used += b.Sub(a)
		}
	}
	return used
}

// CycleWindowStart is where the rolling cycle begins for an instant: local
// midnight (in the home terminal's zone) of the first of the cycle's days,
// today included. A day's hours drop out of the cycle at midnight.
func CycleWindowStart(at time.Time, loc *time.Location, days int) time.Time {
	y, m, d := at.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
}

// EvaluateHOS works out a driver's clocks at now from their active
// records covering [from, now), oldest first (the one in force at from
// may start before it: DutyStatusRepository.ListPeriod). Time before the
// first record counts as rest: a new driver starts rested.
func EvaluateHOS(events []DutyStatusEvent, from, now time.Time, loc *time.Location, cycle HOSCycle) HOSClocks {
	if !cycle.Valid() {
		cycle = HOSCycle70in8
	}
	var w hosWalker
	cursor := from
	for i, e := range events {
		a := maxTime(e.StartedAt, from)
		b := now
		if i+1 < len(events) {
			b = minTime(events[i+1].StartedAt, now)
		}
		if a.After(cursor) { // before the first record: rest
			w.step(kindRest, cursor, minTime(a, now))
		}
		if b.After(a) {
			w.step(kindOf(e.Status), a, b)
			cursor = b
		}
	}
	if now.After(cursor) {
		w.step(kindRest, cursor, now) // no records at all
	}

	limit, days := cycle.Limit()
	c := HOSClocks{At: now, Cycle: cycle, RestStreak: w.restStreak}

	c.DrivingUsed, c.DrivingLeft = 0, MaxDrivingPerShift
	c.WindowLeft = DutyWindow
	if w.shiftStart != nil {
		c.InShift, c.ShiftStart = true, w.shiftStart
		c.DrivingUsed = w.shiftDriving
		c.DrivingLeft = positive(MaxDrivingPerShift - w.shiftDriving)
		end := w.shiftStart.Add(DutyWindow)
		c.WindowEnd = &end
		c.WindowLeft = positive(end.Sub(now))
	}

	c.DrivingSinceBreak = w.sinceBreak
	c.BreakDueIn = positive(BreakAfterDriving - w.sinceBreak)

	c.CycleStart = maxTime(CycleWindowStart(now, loc, days), w.cycleResetAt)
	c.CycleUsed = w.cycleUsed(c.CycleStart, now)
	c.CycleLeft = positive(limit - c.CycleUsed)

	c.CanDriveFor = min(c.DrivingLeft, c.WindowLeft, c.BreakDueIn, c.CycleLeft)
	return c
}

func positive(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
