package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hosDay builds a log from (status, "day hh:mm") pairs in Chicago time;
// day 0 is Tuesday 2026-11-10.
type hosLog struct {
	t   *testing.T
	loc *time.Location
}

func (h hosLog) at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, 10+day, hour, minute, 0, 0, h.loc)
}

func (h hosLog) events(pairs ...any) []DutyStatusEvent {
	h.t.Helper()
	require.Zero(h.t, len(pairs)%2)
	var out []DutyStatusEvent
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, DutyStatusEvent{Status: pairs[i].(DutyStatus), StartedAt: pairs[i+1].(time.Time)})
	}
	return out
}

func (h hosLog) eval(events []DutyStatusEvent, now time.Time) HOSClocks {
	return EvaluateHOS(events, now.Add(-HOSLookback), now, h.loc, HOSCycle70in8)
}

func TestEvaluateHOS_OneShift(t *testing.T) {
	h := hosLog{t, chicago(t)}
	// The example day: rested overnight, then
	//   06:00 on duty (pre-trip)   07:00 driving   11:00 off (lunch, 30 min)
	//   11:30 driving              15:30 on duty (unloading)
	day := h.events(
		DutyOffDuty, h.at(-1, 18, 0),
		DutyOnDuty, h.at(0, 6, 0),
		DutyDriving, h.at(0, 7, 0),
		DutyOffDuty, h.at(0, 11, 0),
		DutyDriving, h.at(0, 11, 30),
		DutyOnDuty, h.at(0, 15, 30),
	)

	c := h.eval(day, h.at(0, 16, 30))

	assert.True(t, c.InShift)
	assert.Equal(t, h.at(0, 6, 0), *c.ShiftStart, "the shift began with the pre-trip, not the first drive")
	assert.Equal(t, 8*time.Hour, c.DrivingUsed)
	assert.Equal(t, 3*time.Hour, c.DrivingLeft, "11 - 8")
	assert.Equal(t, h.at(0, 20, 0), *c.WindowEnd, "06:00 + 14 h")
	assert.Equal(t, 3*time.Hour+30*time.Minute, c.WindowLeft)
	// The hour unloading is a break too: 30 minutes not driving, on duty
	// or not (the rule since 2020).
	assert.Zero(t, c.DrivingSinceBreak, "unloading was a break")
	assert.Equal(t, 8*time.Hour, c.BreakDueIn)
	at1530 := h.eval(day, h.at(0, 15, 30))
	assert.Equal(t, 4*time.Hour, at1530.DrivingSinceBreak, "before unloading: 4 h since lunch")
	assert.Equal(t, 10*time.Hour, c.CycleUsed, "on duty + driving")
	assert.Equal(t, 60*time.Hour, c.CycleLeft)
	assert.Equal(t, 3*time.Hour, c.CanDriveFor, "the tightest: the 11 hours")
	assert.Zero(t, c.RestStreak, "working")
}

func TestEvaluateHOS_TheRules(t *testing.T) {
	h := hosLog{t, chicago(t)}

	t.Run("a new driver has every clock full", func(t *testing.T) {
		c := h.eval(nil, h.at(0, 9, 0))
		assert.False(t, c.InShift)
		assert.Equal(t, 11*time.Hour, c.DrivingLeft)
		assert.Equal(t, 14*time.Hour, c.WindowLeft)
		assert.Equal(t, 8*time.Hour, c.BreakDueIn)
		assert.Equal(t, 70*time.Hour, c.CycleLeft)
		assert.Equal(t, 8*time.Hour, c.CanDriveFor, "the break comes first")
	})

	t.Run("14 hours: breaks do not stop the clock", func(t *testing.T) {
		c := h.eval(h.events(
			DutyOnDuty, h.at(0, 6, 0),
			DutyDriving, h.at(0, 7, 0),
			DutyOffDuty, h.at(0, 10, 0), // a long lunch: 5 hours off
			DutyDriving, h.at(0, 15, 0),
		), h.at(0, 19, 0))

		assert.Equal(t, 7*time.Hour, c.DrivingUsed)
		assert.Equal(t, 4*time.Hour, c.DrivingLeft)
		assert.Equal(t, time.Hour, c.WindowLeft, "06:00 + 14 h = 20:00, lunch or not")
		assert.Equal(t, time.Hour, c.CanDriveFor, "the window, not the 11 hours")
	})

	t.Run("30 minutes: 8 hours of driving without a break", func(t *testing.T) {
		c := h.eval(h.events(
			DutyDriving, h.at(0, 6, 0),
			DutyOnDuty, h.at(0, 9, 0), // 20 minutes fueling: not a break
			DutyDriving, h.at(0, 9, 20),
		), h.at(0, 14, 20))

		assert.Equal(t, 8*time.Hour, c.DrivingSinceBreak)
		assert.Zero(t, c.BreakDueIn)
		assert.Zero(t, c.CanDriveFor, "3 hours of the 11 are left, but not before a break")
		assert.Equal(t, 3*time.Hour, c.DrivingLeft)
	})

	t.Run("30 minutes on duty is a break too", func(t *testing.T) {
		c := h.eval(h.events(
			DutyDriving, h.at(0, 6, 0),
			DutyOnDuty, h.at(0, 12, 0), // 30 minutes loading
			DutyDriving, h.at(0, 12, 30),
		), h.at(0, 13, 0))
		assert.Equal(t, 30*time.Minute, c.DrivingSinceBreak)
	})

	t.Run("10 hours of rest start a new shift; 9:59 does not", func(t *testing.T) {
		yesterday := []any{
			DutyOnDuty, h.at(-1, 6, 0),
			DutyDriving, h.at(-1, 7, 0),
			DutyOffDuty, h.at(-1, 17, 0),
		}
		rested := h.eval(h.events(append(yesterday, DutyOnDuty, h.at(0, 3, 0))...), h.at(0, 4, 0))
		assert.Equal(t, h.at(0, 3, 0), *rested.ShiftStart, "17:00 + 10 h: a new shift at 03:00")
		assert.Equal(t, 11*time.Hour, rested.DrivingLeft)

		short := h.eval(h.events(append(yesterday, DutyOnDuty, h.at(0, 2, 59))...), h.at(0, 4, 0))
		assert.Equal(t, h.at(-1, 6, 0), *short.ShiftStart, "9 h 59 min: still yesterday's shift")
		assert.Equal(t, time.Hour, short.DrivingLeft)
		assert.Zero(t, short.WindowLeft, "and its 14 hours are long gone")
	})

	t.Run("the sleeper and personal conveyance are rest; yard moves are work", func(t *testing.T) {
		c := h.eval(h.events(
			DutyDriving, h.at(-1, 8, 0),
			DutySleeperBerth, h.at(-1, 18, 0), // 6 h in the bunk
			DutyOffDuty, h.at(0, 0, 0), // PC to dinner (off duty) and 4 h more
			DutyOnDuty, h.at(0, 4, 0), // yard moves
		), h.at(0, 5, 0))
		assert.Equal(t, h.at(0, 4, 0), *c.ShiftStart, "6 h sleeper + 4 h off = 10 h rest")
		assert.Equal(t, time.Hour, c.CycleUsed-10*time.Hour, "yesterday's 10 h of driving + 1 h on duty")
	})

	t.Run("resting now: the streak counts toward 10 and 34 hours", func(t *testing.T) {
		c := h.eval(h.events(DutyDriving, h.at(0, 6, 0), DutyOffDuty, h.at(0, 14, 0)), h.at(0, 18, 0))
		assert.Equal(t, 4*time.Hour, c.RestStreak)
		assert.True(t, c.InShift, "4 hours off do not end the shift")
		assert.Equal(t, 2*time.Hour, c.WindowLeft, "06:00 + 14 h = 20:00, resting or not")
	})
}

func TestEvaluateHOS_Cycle(t *testing.T) {
	h := hosLog{t, chicago(t)}
	// A working week: 9 hours on duty a day (1 h on duty, 8 h driving), the
	// rest off, for 8 days: day -7 .. day 0.
	week := func(days int) []any {
		var pairs []any
		for d := -days + 1; d <= 0; d++ {
			pairs = append(pairs,
				DutyOnDuty, h.at(d, 6, 0),
				DutyDriving, h.at(d, 7, 0),
				DutyOffDuty, h.at(d, 15, 0))
		}
		return pairs
	}

	t.Run("70 hours in 8 days", func(t *testing.T) {
		c := h.eval(h.events(week(8)...), h.at(0, 20, 0))

		assert.Equal(t, 72*time.Hour, c.CycleUsed, "8 days x 9 h")
		assert.Zero(t, c.CycleLeft, "over the 70")
		assert.Zero(t, c.CanDriveFor)
		// Today and the 7 days before, from midnight; but nothing before the
		// first record counts (it is rest, longer than 34 h: a restart).
		assert.Equal(t, h.at(-7, 6, 0), c.CycleStart)
	})

	t.Run("at midnight the oldest day drops out", func(t *testing.T) {
		c := h.eval(h.events(week(8)...), h.at(1, 0, 30))
		assert.Equal(t, 63*time.Hour, c.CycleUsed, "day -7 is out: 7 x 9 h")
		assert.Equal(t, 7*time.Hour, c.CycleLeft)
	})

	t.Run("60 hours in 7 days", func(t *testing.T) {
		c := EvaluateHOS(h.events(week(8)...), h.at(0, 20, 0).Add(-HOSLookback), h.at(0, 20, 0), h.loc, HOSCycle60in7)
		assert.Equal(t, 63*time.Hour, c.CycleUsed, "7 x 9 h")
		assert.Zero(t, c.CycleLeft)
	})

	t.Run("34 hours off restart the cycle", func(t *testing.T) {
		pairs := week(5)                                 // days -4 .. 0: 45 h
		pairs = append(pairs, DutyOnDuty, h.at(2, 3, 0)) // off from day 0 15:00 to day 2 03:00 = 36 h
		pairs = append(pairs, DutyDriving, h.at(2, 4, 0))
		c := h.eval(h.events(pairs...), h.at(2, 6, 0))

		assert.Equal(t, 3*time.Hour, c.CycleUsed, "only what came after the restart")
		assert.Equal(t, h.at(2, 3, 0), c.CycleStart)
	})

	t.Run("33 hours do not", func(t *testing.T) {
		pairs := append(week(5), DutyOnDuty, h.at(2, 0, 0)) // 33 h off
		c := h.eval(h.events(pairs...), h.at(2, 1, 0))
		assert.Equal(t, 46*time.Hour, c.CycleUsed, "45 h + this hour")
	})
}
