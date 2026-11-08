package eldsim

import (
	"math"
	"time"
)

// PhaseKind is what the truck does during a phase of its trip.
type PhaseKind int

const (
	Idle  PhaseKind = iota // parked, engine running (inspection, waiting)
	Drive                  // driving to a mile marker on the route
	Park                   // parked, engine off
)

// Phase is one part of a trip.
type Phase struct {
	Kind     PhaseKind
	Duration time.Duration // Idle, Park: how long
	ToMile   float64       // Drive: where to stop
	Note     string        // shown in the simulator's output
}

// DefaultTrip is a working day on the route: pre-trip inspection, drive,
// a fuel stop with the engine off, drive on, unload, engine off.
func DefaultTrip(r *Route) []Phase {
	half := r.Length() * 0.4
	return []Phase{
		{Kind: Idle, Duration: 3 * time.Minute, Note: "pre-trip inspection"},
		{Kind: Drive, ToMile: half, Note: "driving"},
		{Kind: Park, Duration: 20 * time.Minute, Note: "fuel stop, engine off"},
		{Kind: Idle, Duration: 2 * time.Minute, Note: "warming up"},
		{Kind: Drive, ToMile: r.Length(), Note: "driving"},
		{Kind: Idle, Duration: 5 * time.Minute, Note: "unloading"},
		{Kind: Park, Duration: 0, Note: "arrived, engine off"},
	}
}

// Truck is the vehicle the ELD sits in: it follows a trip plan and keeps
// the readings an ELD reads from the engine (speed, odometer, engine hours).
type Truck struct {
	Route *Route
	Plan  []Phase

	CruiseMPH float64 // highway speed
	AccelMPHs float64 // speed change per second, up and down

	Mile        float64 // where on the route
	SpeedMPH    float64
	OdometerMi  float64
	EngineHours float64
	EngineOn    bool

	phase   int
	elapsed time.Duration // within the current phase
}

// NewTruck starts at the beginning of the route with the given odometer and
// engine hours (a real truck is not new: they continue from the last run).
func NewTruck(r *Route, plan []Phase, odometerMi, engineHours float64) *Truck {
	t := &Truck{Route: r, Plan: plan, CruiseMPH: 62, AccelMPHs: 1.5, OdometerMi: odometerMi, EngineHours: engineHours}
	t.enter()
	return t
}

// Done: the last phase has started and is a Park, or the plan ran out.
func (t *Truck) Done() bool {
	return t.phase >= len(t.Plan) || (t.phase == len(t.Plan)-1 && t.Plan[t.phase].Kind == Park)
}

// Phase is the phase the truck is in now.
func (t *Truck) Phase() Phase {
	if t.phase >= len(t.Plan) {
		return Phase{Kind: Park, Note: "done"}
	}
	return t.Plan[t.phase]
}

func (t *Truck) Position() LatLon { return t.Route.At(t.Mile) }

// Step advances the truck by dt, one simulated second at a time: speed and
// distance change together, and a long dt cannot jump past a stop.
func (t *Truck) Step(dt time.Duration) {
	for s := dt; s > 0 && !t.Done(); s -= time.Second {
		sec := time.Second
		if s < sec {
			sec = s
		}
		t.second(sec)
	}
}

func (t *Truck) second(dt time.Duration) {
	p := t.Plan[t.phase]
	hours := dt.Hours()
	if t.EngineOn {
		t.EngineHours += hours
	}

	switch p.Kind {
	case Idle, Park:
		t.elapsed += dt
		if t.elapsed >= p.Duration {
			t.next()
		}
	case Drive:
		remaining := p.ToMile - t.Mile
		// Brake in time: the distance to stop from v at a constant
		// deceleration a is v²/(2a); speeds in mph, a in mph/s, so divide
		// by 3600 to get miles.
		stopping := t.SpeedMPH * t.SpeedMPH / (2 * t.AccelMPHs) / 3600
		target := t.CruiseMPH
		if remaining <= stopping {
			target = 0
		}
		v0 := t.SpeedMPH
		step := t.AccelMPHs * dt.Seconds()
		t.SpeedMPH = math.Max(math.Min(target, v0+step), v0-step)
		if t.SpeedMPH < 0 {
			t.SpeedMPH = 0
		}
		// Distance at the average speed over the step.
		d := math.Min((v0+t.SpeedMPH)/2*hours, remaining)
		if t.SpeedMPH == 0 && remaining > 0 && d == 0 {
			// Stopped short (rounding): creep the rest at walking pace.
			t.SpeedMPH, d = 3, math.Min(3*hours, remaining)
		}
		t.Mile += d
		t.OdometerMi += d
		if p.ToMile-t.Mile < 1e-6 {
			t.Mile, t.SpeedMPH = p.ToMile, 0
			t.next()
		}
	}
}

func (t *Truck) next() {
	t.phase++
	t.enter()
}

func (t *Truck) enter() {
	t.elapsed = 0
	if t.phase < len(t.Plan) {
		t.EngineOn = t.Plan[t.phase].Kind != Park
	}
}

// TripDuration measures how long the plan takes, by driving a copy of the
// truck to the end. The simulator needs it to time the trip (see Run).
func TripDuration(r *Route, plan []Phase) time.Duration {
	t := NewTruck(r, plan, 0, 0)
	var d time.Duration
	for !t.Done() {
		t.Step(time.Minute)
		d += time.Minute
	}
	return d
}
