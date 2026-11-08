package eldsim

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistanceMiles(t *testing.T) {
	dallas, houston := LatLon{32.7767, -96.7970}, LatLon{29.7604, -95.3698}
	assert.InDelta(t, 225, DistanceMiles(dallas, houston), 5, "as the crow flies: ~225 miles")
	assert.Zero(t, DistanceMiles(dallas, dallas))
	// One degree of latitude is ~69 miles anywhere.
	assert.InDelta(t, 69.1, DistanceMiles(LatLon{30, -95}, LatLon{31, -95}), 0.2)
}

func TestRoute(t *testing.T) {
	r, err := BuiltInRoute("dallas-houston")
	require.NoError(t, err)
	assert.InDelta(t, 240, r.Length(), 15, "I-45 bends: a bit longer than straight")

	assert.Equal(t, r.Points[0], r.At(-1), "clamped to the start")
	assert.Equal(t, r.Points[0], r.At(0))
	assert.Equal(t, r.Points[len(r.Points)-1], r.At(r.Length()+50), "clamped to the end")

	// Exactly at a waypoint, and halfway along the first segment.
	assert.InDelta(t, r.Points[2].Lat, r.At(r.cum[2]).Lat, 1e-9)
	mid := r.At(r.cum[1] / 2)
	assert.InDelta(t, (r.Points[0].Lat+r.Points[1].Lat)/2, mid.Lat, 1e-9)
}

func TestRoute_Errors(t *testing.T) {
	_, err := BuiltInRoute("moon")
	assert.ErrorContains(t, err, "dallas-houston", "the error lists what exists")
	_, err = NewRoute("dot", []LatLon{{1, 1}})
	assert.Error(t, err)
}
