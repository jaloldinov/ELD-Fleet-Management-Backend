package model

import (
	"slices"
	"time"
)

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

// HOSRule names a limit a driver can break.
type HOSRule string

const (
	RuleDriving11h HOSRule = "driving_11h" // drove past 11 hours in the shift
	RuleWindow14h  HOSRule = "window_14h"  // drove after the 14th hour of the shift
	RuleBreak30m   HOSRule = "break_30m"   // drove past 8 hours without a 30-minute break
	RuleCycle      HOSRule = "cycle"       // drove past 60/70 hours on duty in the cycle
)

// HOSViolation: driving in [Start, End) broke Rule. Only driving breaks a
// limit: a driver out of hours may still work on duty or rest.
type HOSViolation struct {
	Rule       HOSRule
	Start, End time.Time
}

func (v HOSViolation) Duration() time.Duration { return v.End.Sub(v.Start) }

// HOSViolationsShownFor: the clocks list the violations of the last 8
// days (what an inspector looks at). Older ones may be wrong anyway: the
// engine does not know what came before its lookback.
const HOSViolationsShownFor = 8 * 24 * time.Hour

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

	// Violations in [At - HOSViolationsShownFor, At), oldest first.
	Violations []HOSViolation
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

	loc        *time.Location
	cycleLimit time.Duration
	cycleDays  int
	violations []HOSViolation
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
	if k == kindDriving {
		w.checkDriving(a, b) // with the clocks as they stood at a
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

// checkDriving records where driving in [a, b) breaks a limit. For each
// limit: the instant it runs out, from the clocks at a; driving after it
// is the violation. [a, b) never crosses local midnight (stepByDay), so
// the cycle's first day stays the same throughout.
func (w *hosWalker) checkDriving(a, b time.Time) {
	cycleStart := maxTime(CycleWindowStart(a, w.loc, w.cycleDays), w.cycleResetAt)
	runsOut := map[HOSRule]time.Time{
		RuleDriving11h: a.Add(MaxDrivingPerShift - w.shiftDriving),
		RuleWindow14h:  w.shiftStart.Add(DutyWindow),
		RuleBreak30m:   a.Add(BreakAfterDriving - w.sinceBreak),
		RuleCycle:      a.Add(w.cycleLimit - w.cycleUsed(cycleStart, a)),
	}
	for _, rule := range []HOSRule{RuleDriving11h, RuleWindow14h, RuleBreak30m, RuleCycle} {
		if t := runsOut[rule]; t.Before(b) {
			w.violate(rule, maxTime(t, a), b)
		}
	}
}

// violate adds a violation, joining it to the previous one of the same
// rule when they touch (a drive cut at midnight is one violation).
func (w *hosWalker) violate(rule HOSRule, a, b time.Time) {
	for i := len(w.violations) - 1; i >= 0; i-- {
		if v := &w.violations[i]; v.Rule == rule {
			if v.End.Equal(a) {
				v.End = b
				return
			}
			break
		}
	}
	w.violations = append(w.violations, HOSViolation{Rule: rule, Start: a, End: b})
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
	limit, days := cycle.Limit()
	w := hosWalker{loc: loc, cycleLimit: limit, cycleDays: days}
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
			w.stepByDay(kindOf(e.Status), a, b)
			cursor = b
		}
	}
	if now.After(cursor) {
		w.step(kindRest, cursor, now) // no records at all
	}

	c := HOSClocks{At: now, Cycle: cycle, RestStreak: w.restStreak}
	shownFrom := now.Add(-HOSViolationsShownFor)
	for _, v := range w.violations {
		if v.End.After(shownFrom) {
			v.Start = maxTime(v.Start, shownFrom)
			c.Violations = append(c.Violations, v)
		}
	}
	// One drive can break several rules: list them by when they began.
	slices.SortStableFunc(c.Violations, func(x, y HOSViolation) int { return x.Start.Compare(y.Start) })

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

// stepByDay cuts a segment at local midnights: the cycle's window moves
// at midnight, and each piece must see one window.
func (w *hosWalker) stepByDay(k dutyKind, a, b time.Time) {
	for a.Before(b) {
		y, m, d := a.In(w.loc).Date()
		midnight := time.Date(y, m, d+1, 0, 0, 0, 0, w.loc)
		end := minTime(midnight, b)
		w.step(k, a, end)
		a = end
	}
}

func positive(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
