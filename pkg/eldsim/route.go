// Package eldsim simulates an ELD device in a truck: it "drives" a route,
// produces readings (GPS, speed, odometer, engine hours) and sends them to
// the API like a real device would, including buffering while offline.
//
// It replaces hardware during development (cmd/eldsim) and in end-to-end
// tests: everything time-related goes through a Clock, so tests run a
// four-hour trip in milliseconds.
package eldsim

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// LatLon is a point on Earth, in degrees.
type LatLon struct {
	Lat, Lon float64
}

// Route is a road as a polyline: the truck follows the straight segments
// between the points. Good enough for a simulator; real roads curve.
type Route struct {
	Name   string
	Points []LatLon
	cum    []float64 // cum[i]: miles from the start to Points[i]
}

// NewRoute measures the route once, so positions along it are cheap.
func NewRoute(name string, points []LatLon) (*Route, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("route %s: needs at least 2 points", name)
	}
	r := &Route{Name: name, Points: points, cum: make([]float64, len(points))}
	for i := 1; i < len(points); i++ {
		r.cum[i] = r.cum[i-1] + DistanceMiles(points[i-1], points[i])
	}
	return r, nil
}

// Length is the route's length in miles.
func (r *Route) Length() float64 { return r.cum[len(r.cum)-1] }

// At returns the position after travelling d miles from the start
// (clamped to the route): find the segment, then go the remaining fraction
// of it in a straight line.
func (r *Route) At(d float64) LatLon {
	if d <= 0 {
		return r.Points[0]
	}
	if d >= r.Length() {
		return r.Points[len(r.Points)-1]
	}
	// The first point at or beyond d ends our segment.
	i := sort.SearchFloat64s(r.cum, d)
	a, b := r.Points[i-1], r.Points[i]
	f := (d - r.cum[i-1]) / (r.cum[i] - r.cum[i-1])
	return LatLon{Lat: a.Lat + f*(b.Lat-a.Lat), Lon: a.Lon + f*(b.Lon-a.Lon)}
}

const earthRadiusMiles = 3958.8

// DistanceMiles is the great-circle distance between two points (the
// haversine formula): the shortest way over the Earth's surface.
func DistanceMiles(a, b LatLon) float64 {
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat, dLon := rad(b.Lat-a.Lat), rad(b.Lon-a.Lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusMiles * math.Asin(math.Sqrt(h))
}

// Built-in routes: Texas interstates, where many US carriers run.
var builtIn = map[string][]LatLon{
	// I-45 south, about 240 miles.
	"dallas-houston": {
		{32.7767, -96.7970}, // Dallas
		{32.3293, -96.6253}, // Ennis
		{32.0954, -96.4689}, // Corsicana
		{31.7240, -96.1652}, // Fairfield
		{31.4635, -96.0580}, // Buffalo
		{30.9502, -95.9116}, // Madisonville
		{30.7235, -95.5508}, // Huntsville
		{30.3119, -95.4561}, // Conroe
		{29.7604, -95.3698}, // Houston
	},
	// A short loop for quick tries, about 20 miles.
	"dallas-loop": {
		{32.7767, -96.7970}, {32.8998, -96.8000}, {32.9000, -96.6500},
		{32.7700, -96.6500}, {32.7767, -96.7970},
	},
}

// BuiltInRoute returns one of the built-in routes by name.
func BuiltInRoute(name string) (*Route, error) {
	pts, ok := builtIn[name]
	if !ok {
		names := make([]string, 0, len(builtIn))
		for n := range builtIn {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown route %q (have: %s)", name, strings.Join(names, ", "))
	}
	return NewRoute(name, pts)
}
