package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDrivingIntervals(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	at := func(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }
	ev := func(s DutyStatus, h float64) DutyStatusEvent { return DutyStatusEvent{Status: s, StartedAt: at(h)} }
	day := []DutyStatusEvent{
		ev(DutyOnDuty, 0), ev(DutyDriving, 1), ev(DutyOffDuty, 5), ev(DutyDriving, 5.5), ev(DutyOnDuty, 9.5),
	}

	assert.Equal(t, []Interval{{at(1), at(5)}, {at(5.5), at(9.5)}}, DrivingIntervals(day, at(0), at(12)))
	assert.Equal(t, []Interval{{at(2), at(5)}, {at(5.5), at(8)}}, DrivingIntervals(day, at(2), at(8)), "cut to the window")
	assert.Equal(t, []Interval{{at(1), at(3)}},
		DrivingIntervals([]DutyStatusEvent{ev(DutyDriving, 1), ev(DutyDriving, 2), ev(DutyOffDuty, 3)}, at(0), at(12)),
		"back to back: one interval")
	assert.Empty(t, DrivingIntervals([]DutyStatusEvent{ev(DutyOffDuty, 0)}, at(0), at(12)))

	// The edits the comparison catches.
	before := DrivingIntervals(day, at(0), at(12))
	shortened := []DutyStatusEvent{ev(DutyOnDuty, 0), ev(DutyDriving, 1), ev(DutyOffDuty, 4), ev(DutyDriving, 5.5), ev(DutyOnDuty, 9.5)}
	assert.False(t, SameIntervals(before, DrivingIntervals(shortened, at(0), at(12))), "the off duty moved into the drive")
	split := append(append([]DutyStatusEvent{}, day[:2]...), append([]DutyStatusEvent{ev(DutySleeperBerth, 3)}, day[2:]...)...)
	assert.False(t, SameIntervals(before, DrivingIntervals(split, at(0), at(12))), "a record added inside a drive")
	relabelled := []DutyStatusEvent{ev(DutyOffDuty, 0), ev(DutyDriving, 1), ev(DutySleeperBerth, 5), ev(DutyDriving, 5.5), ev(DutyOnDuty, 9.5)}
	assert.True(t, SameIntervals(before, DrivingIntervals(relabelled, at(0), at(12))), "changing what was NOT driving is fine")
}

func TestDailyLog_Certified(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	log := DailyLog{Segments: []DutySegment{{Event: DutyStatusEvent{CreatedAt: t0}}}}
	log.SetHistory(nil)
	assert.False(t, log.Certified(), "never signed")

	signed := t0.Add(time.Hour)
	log.CertifiedAt = &signed
	assert.True(t, log.Certified())

	edited := signed.Add(time.Minute)
	log.SetHistory([]DutyStatusEvent{{InactivatedAt: &edited}})
	assert.Equal(t, edited, log.LastChangeAt)
	assert.False(t, log.Certified(), "changed after the signature: sign again")
}
