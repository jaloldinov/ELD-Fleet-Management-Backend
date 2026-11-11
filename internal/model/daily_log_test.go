package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	return loc
}

// sumsUp: the grid always adds up to the time it covers.
func sumsUp(t *testing.T, log DailyLog) {
	t.Helper()
	var total time.Duration
	for _, d := range log.Totals {
		total += d
	}
	assert.Equal(t, log.Until.Sub(log.Start), total+log.Unrecorded, "totals + unrecorded = the day so far")
	for i := 1; i < len(log.Segments); i++ {
		assert.Equal(t, log.Segments[i-1].End, log.Segments[i].Start, "no gaps, no overlaps")
	}
}

func TestDayBounds(t *testing.T) {
	loc := chicago(t)

	start, end, err := DayBounds("2026-11-10", loc)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-10T00:00:00-06:00", start.Format(time.RFC3339))
	assert.Equal(t, 24*time.Hour, end.Sub(start))

	start, end, err = DayBounds("2026-11-01", loc) // clocks go back an hour at 02:00
	require.NoError(t, err)
	assert.Equal(t, 25*time.Hour, end.Sub(start))

	start, end, err = DayBounds("2026-03-08", loc) // clocks go forward
	require.NoError(t, err)
	assert.Equal(t, 23*time.Hour, end.Sub(start))

	for _, bad := range []string{"", "2026-13-01", "11/10/2026", "2026-11-10T00:00:00Z"} {
		_, _, err := DayBounds(bad, loc)
		assert.Error(t, err, bad)
	}
}

func TestBuildDailyLog(t *testing.T) {
	loc := chicago(t)
	start, end, err := DayBounds("2026-11-10", loc)
	require.NoError(t, err)
	at := func(h, m int) time.Time { return start.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	ev := func(s DutyStatus, t time.Time) DutyStatusEvent { return DutyStatusEvent{Status: s, StartedAt: t} }
	afterTheDay := end.Add(time.Hour)

	t.Run("the example day: 8 h driving, 2 h on duty, the rest off", func(t *testing.T) {
		log := BuildDailyLog([]DutyStatusEvent{
			ev(DutyOffDuty, at(-4, 0)), // yesterday 20:00
			ev(DutyOnDuty, at(6, 0)),
			ev(DutyDriving, at(7, 0)),
			ev(DutyOffDuty, at(11, 0)),
			ev(DutyDriving, at(11, 30)),
			ev(DutyOnDuty, at(15, 30)),
			ev(DutyOffDuty, at(16, 30)),
		}, start, end, afterTheDay)

		sumsUp(t, log)
		require.Len(t, log.Segments, 7)
		assert.Equal(t, start, log.Segments[0].Start, "yesterday's off duty covers 00:00-06:00")
		assert.Equal(t, 6*time.Hour, log.Segments[0].Duration())
		assert.Equal(t, end, log.Segments[6].End, "the last one runs to midnight")
		assert.Equal(t, 8*time.Hour, log.Totals[DutyDriving])
		assert.Equal(t, 2*time.Hour, log.Totals[DutyOnDuty])
		assert.Equal(t, 14*time.Hour, log.Totals[DutyOffDuty])
		assert.Zero(t, log.Totals[DutySleeperBerth])
		assert.Zero(t, log.Unrecorded)
	})

	t.Run("today: known up to now", func(t *testing.T) {
		log := BuildDailyLog([]DutyStatusEvent{ev(DutyOffDuty, at(-4, 0)), ev(DutyDriving, at(7, 0))}, start, end, at(9, 15))

		sumsUp(t, log)
		assert.Equal(t, at(9, 15), log.Until)
		assert.Equal(t, 2*time.Hour+15*time.Minute, log.Totals[DutyDriving], "driving so far")
	})

	t.Run("a new driver: the day before the first record is unrecorded", func(t *testing.T) {
		log := BuildDailyLog([]DutyStatusEvent{ev(DutyOnDuty, at(8, 0))}, start, end, afterTheDay)

		sumsUp(t, log)
		assert.Equal(t, 8*time.Hour, log.Unrecorded)
		assert.Equal(t, 16*time.Hour, log.Totals[DutyOnDuty])
	})

	t.Run("no records at all", func(t *testing.T) {
		log := BuildDailyLog(nil, start, end, afterTheDay)
		sumsUp(t, log)
		assert.Empty(t, log.Segments)
		assert.Equal(t, 24*time.Hour, log.Unrecorded)
	})

	t.Run("a day that has not begun is empty", func(t *testing.T) {
		log := BuildDailyLog([]DutyStatusEvent{ev(DutyOffDuty, at(-4, 0))}, start, end, start.Add(-time.Minute))
		sumsUp(t, log)
		assert.Empty(t, log.Segments)
	})

	t.Run("a record from the future of 'now' is not shown yet", func(t *testing.T) {
		// Not reachable through the API today, but the cut must hold.
		log := BuildDailyLog([]DutyStatusEvent{ev(DutyOffDuty, at(-4, 0)), ev(DutyDriving, at(10, 0))}, start, end, at(9, 0))
		sumsUp(t, log)
		assert.Len(t, log.Segments, 1)
	})

	t.Run("personal conveyance sits on the off-duty line", func(t *testing.T) {
		pc := SpecialPersonalConveyance
		log := BuildDailyLog([]DutyStatusEvent{
			ev(DutyOnDuty, at(-1, 0)),
			{Status: DutyOffDuty, Special: &pc, StartedAt: at(20, 0)},
		}, start, end, afterTheDay)
		sumsUp(t, log)
		assert.Equal(t, 4*time.Hour, log.Totals[DutyOffDuty])
		assert.Zero(t, log.Totals[DutyDriving], "moving, but not driving")
	})

	t.Run("the 25-hour day", func(t *testing.T) {
		s, e, err := DayBounds("2026-11-01", loc)
		require.NoError(t, err)
		log := BuildDailyLog([]DutyStatusEvent{ev(DutySleeperBerth, s.Add(-time.Hour))}, s, e, e.Add(time.Hour))
		sumsUp(t, log)
		assert.Equal(t, 25*time.Hour, log.Totals[DutySleeperBerth])
	})
}
