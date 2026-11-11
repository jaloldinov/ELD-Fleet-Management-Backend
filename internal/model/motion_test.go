package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drive feeds readings (minute, mph) through Step from no state and
// returns every change and the final run.
func drive(t0 time.Time, readings ...[2]float64) (*Motion, []MotionChange) {
	var m *Motion
	var all []MotionChange
	for _, r := range readings {
		p := TelemetryPoint{RecordedAt: t0.Add(time.Duration(r[0] * float64(time.Minute))), SpeedMPH: r[1]}
		next, changes := m.Step(p)
		m = &next
		all = append(all, changes...)
	}
	return m, all
}

func kinds(changes []MotionChange, t0 time.Time) []string {
	var out []string
	for _, c := range changes {
		out = append(out, string(c.Kind)+"@"+c.At.Sub(t0).String())
	}
	return out
}

func TestMotion_Step(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)

	t.Run("standing still from the start: no drive", func(t *testing.T) {
		m, changes := drive(t0, [2]float64{0, 0}, [2]float64{1, 3}, [2]float64{10, 0})
		// The stop is still confirmed: Motion only knows the truck, not the
		// driver. The logbook ignores it for a driver who is not driving.
		assert.Equal(t, []string{"stop_confirmed@5m0s"}, kinds(changes, t0))
		assert.False(t, m.Moving)
		assert.Equal(t, t0, m.Since)
		assert.Equal(t, t0.Add(10*time.Minute), m.Last)
	})

	t.Run("the first reading already moving starts driving", func(t *testing.T) {
		_, changes := drive(t0, [2]float64{0, 40})
		assert.Equal(t, []string{"drive_started@0s"}, kinds(changes, t0))
	})

	t.Run("drive, short stop, drive on: no stop confirmed", func(t *testing.T) {
		m, changes := drive(t0,
			[2]float64{0, 0}, [2]float64{1, 30}, [2]float64{2, 60},
			[2]float64{3, 0}, [2]float64{6, 0}, // a red light... a long one: 4 minutes
			[2]float64{7, 45})
		// Two starts, no confirmed stop between them: the logbook keeps the
		// driver in the first driving status (it ignores a start while driving).
		assert.Equal(t, []string{"drive_started@1m0s", "drive_started@7m0s"}, kinds(changes, t0))
		assert.True(t, m.Moving)
		assert.Equal(t, t0.Add(7*time.Minute), m.Since)
	})

	t.Run("a reading past the 5-minute mark confirms the stop once", func(t *testing.T) {
		_, changes := drive(t0,
			[2]float64{0, 50},
			[2]float64{10, 0}, [2]float64{14, 0}, [2]float64{15, 0}, [2]float64{20, 0}, [2]float64{30, 0})
		assert.Equal(t, []string{"drive_started@0s", "stop_confirmed@15m0s"}, kinds(changes, t0),
			"exactly at the mark counts; later readings add nothing")
	})

	t.Run("quiet during the stop: the next drive confirms it first", func(t *testing.T) {
		_, changes := drive(t0, [2]float64{0, 50}, [2]float64{10, 0}, [2]float64{40, 55})
		assert.Equal(t, []string{"drive_started@0s", "stop_confirmed@15m0s", "drive_started@40m0s"}, kinds(changes, t0),
			"oldest first: the stop ends the first drive before the second starts")
	})

	t.Run("5 mph is standing, above is moving", func(t *testing.T) {
		_, changes := drive(t0, [2]float64{0, 5}, [2]float64{1, 5.1})
		assert.Equal(t, []string{"drive_started@1m0s"}, kinds(changes, t0))
	})

	t.Run("the change carries the reading that tells", func(t *testing.T) {
		_, changes := drive(t0, [2]float64{0, 50}, [2]float64{10, 0}, [2]float64{16, 0})
		require.Len(t, changes, 2)
		assert.Equal(t, t0.Add(16*time.Minute), changes[1].Point.RecordedAt)
	})
}

func TestMotion_Silence(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	stopped := Motion{Moving: false, Since: t0, Last: t0.Add(time.Minute)} // then the engine went off

	_, ok := stopped.Silence(t0.Add(4 * time.Minute))
	assert.False(t, ok, "not 5 minutes yet")

	due, ok := stopped.Silence(t0.Add(5 * time.Minute))
	assert.True(t, ok)
	assert.Equal(t, t0.Add(5*time.Minute), due)

	crossed := Motion{Moving: false, Since: t0, Last: t0.Add(6 * time.Minute)}
	_, ok = crossed.Silence(t0.Add(time.Hour))
	assert.False(t, ok, "a reading after the mark already confirmed it (Step)")

	_, ok = Motion{Moving: true, Since: t0, Last: t0}.Silence(t0.Add(time.Hour))
	assert.False(t, ok, "a device quiet while moving says nothing about a stop")
}
