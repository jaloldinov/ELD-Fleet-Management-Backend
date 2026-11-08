package eldsim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruck_DrivesTheRoute(t *testing.T) {
	r, err := BuiltInRoute("dallas-houston")
	require.NoError(t, err)
	tr := NewTruck(r, DefaultTrip(r), 120000, 8000)

	assert.True(t, tr.EngineOn, "starts with the pre-trip inspection")
	assert.Zero(t, tr.SpeedMPH)

	maxSpeed, sawStop := 0.0, false
	for !tr.Done() {
		before := tr.SpeedMPH
		tr.Step(10 * time.Second)
		assert.LessOrEqual(t, tr.SpeedMPH-before, tr.AccelMPHs*10+1e-9, "no jumps in speed")
		if tr.SpeedMPH > maxSpeed {
			maxSpeed = tr.SpeedMPH
		}
		if tr.Phase().Kind == Park && !tr.Done() {
			sawStop = true
			assert.False(t, tr.EngineOn, "engine off at the fuel stop")
			assert.Zero(t, tr.SpeedMPH)
		}
	}

	assert.Equal(t, 62.0, maxSpeed, "cruise speed")
	assert.True(t, sawStop)
	assert.InDelta(t, r.Length(), tr.Mile, 1e-6, "arrived at the end of the route")
	assert.InDelta(t, 120000+r.Length(), tr.OdometerMi, 1e-6, "the odometer counted every mile")
	assert.Equal(t, r.Points[len(r.Points)-1], tr.Position())
	assert.False(t, tr.EngineOn, "arrived: engine off")
	assert.Greater(t, tr.EngineHours, 8000+3.5, "~4h of driving and idling added")
}

func TestTruck_StopsExactlyAtTheFuelStop(t *testing.T) {
	r, err := BuiltInRoute("dallas-houston")
	require.NoError(t, err)
	plan := DefaultTrip(r)
	tr := NewTruck(r, plan, 0, 0)

	for tr.Phase().Kind != Park {
		tr.Step(time.Second)
	}
	assert.InDelta(t, plan[1].ToMile, tr.Mile, 1e-6, "braked in time, did not overshoot")
}

func TestTripDuration(t *testing.T) {
	r, err := BuiltInRoute("dallas-houston")
	require.NoError(t, err)

	d := TripDuration(r, DefaultTrip(r))

	// ~240 miles at up to 62 mph plus 30 minutes of stops.
	assert.Greater(t, d, 4*time.Hour)
	assert.Less(t, d, 5*time.Hour)
}
