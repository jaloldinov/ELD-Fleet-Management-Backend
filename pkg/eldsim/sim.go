package eldsim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Clock is the simulator's sense of time. The real one sleeps; tests use a
// fake that jumps, so a four-hour trip runs in milliseconds.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Window is a stretch of the trip without network (a tunnel, a dead zone),
// as offsets from the trip's start in simulated time: [From, To).
type Window struct {
	From, To time.Duration
}

// ParseWindows reads "30m-45m,2h-2h10m".
func ParseWindows(s string) ([]Window, error) {
	var out []Window
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		from, to, ok := strings.Cut(part, "-")
		if !ok {
			return nil, fmt.Errorf("offline window %q: want FROM-TO, e.g. 30m-45m", part)
		}
		f, err1 := time.ParseDuration(from)
		t, err2 := time.ParseDuration(to)
		if err := errors.Join(err1, err2); err != nil || t <= f {
			return nil, fmt.Errorf("offline window %q: want FROM-TO with FROM < TO, e.g. 30m-45m", part)
		}
		out = append(out, Window{From: f, To: t})
	}
	return out, nil
}

func offline(ws []Window, at time.Duration) bool {
	for _, w := range ws {
		if at >= w.From && at < w.To {
			return true
		}
	}
	return false
}

// Config describes one simulated trip.
type Config struct {
	Client  *Client
	Truck   *Truck
	Memory  *Memory
	Clock   Clock
	Speedup float64       // 60: one real second is one simulated minute
	Sample  time.Duration // simulated time between readings (engine on)
	// Simulated time between sends while online. Readings wait in the
	// buffer meanwhile, as on a real device that saves battery and data.
	SendEvery time.Duration
	Heartbeat time.Duration // simulated time between heartbeats, engine off
	Offline   []Window
	// NotBefore: no reading may be stamped earlier (zero = no limit). Set
	// it to the later of: when the device was installed in its truck
	// (earlier readings would belong to no truck) and the memory's
	// LastRecordedAt (a new trip cannot go back into time already covered).
	NotBefore time.Time
	Log       func(format string, args ...any) // nil = quiet
}

// Stats sums up a run.
type Stats struct {
	Speedup float64 // the speedup actually used (see FitSpeedup)

	Readings, Batches, Heartbeats  int
	Accepted, Duplicates, Rejected int
	MaxBuffered                    int
	FirstRecorded, LastRecorded    time.Time
}

// MaxBatch matches the API's limit per request.
const MaxBatch = 500

// Run drives the trip and reports it to the API like a real ELD.
//
// Timing: the trip is timed to END at the real time it ends. With speedup
// N, a trip of D simulated time takes D/N real time, so it starts at
//
//	start = now − D·(1 − 1/N)
//
// Simulated time then runs N times faster than real time and catches up
// exactly at the end: no reading is ever stamped in the future, which the
// API would (rightly) refuse. With N = 1 it is simply real time from now.
func Run(ctx context.Context, cfg Config) (*Stats, error) {
	if cfg.Speedup < 1 {
		return nil, errors.New("speedup must be at least 1")
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	tr := cfg.Truck
	total := TripDuration(tr.Route, tr.Plan)
	realStart := cfg.Clock.Now()
	speedup := cfg.Speedup
	if !cfg.NotBefore.IsZero() {
		speedup = FitSpeedup(total, cfg.Speedup, realStart.Sub(cfg.NotBefore))
	}
	simStart := realStart.Add(-time.Duration(float64(total) * (1 - 1/speedup)))

	var (
		stats         = Stats{Speedup: speedup}
		buffer        []Reading
		sim           time.Duration // simulated time since the trip started
		lastSend      time.Duration
		lastHeartbeat time.Duration = -cfg.Heartbeat
		wasOffline    bool
		engineWasOn   bool
	)

	flush := func() error {
		for len(buffer) > 0 {
			n := min(len(buffer), MaxBatch)
			// The device's clock is the real one (synced by GPS); only the
			// readings' times are replayed.
			res, err := cfg.Client.Send(ctx, cfg.Clock.Now(), buffer[:n])
			if errors.Is(err, ErrFatal) {
				return err
			}
			if err != nil {
				// Network or server trouble: keep everything, try later.
				logf("send failed, keeping %d readings: %v", len(buffer), err)
				return nil
			}
			// Every reading in the batch got a final answer: drop them all.
			if res.Accepted == 0 && res.Duplicates == n {
				logf("all %d readings were duplicates: was the device memory (-state-dir) lost? "+
					"Its seq restarted below what the API already has.", n)
			}
			buffer = buffer[n:]
			stats.Batches++
			stats.Accepted += res.Accepted
			stats.Duplicates += res.Duplicates
			stats.Rejected += len(res.Rejected)
			for _, r := range res.Rejected {
				logf("reading %d rejected: %s", *r.Seq, r.Reason)
			}
		}
		return cfg.Memory.remember(tr)
	}

	for {
		now := simStart.Add(sim)
		off := offline(cfg.Offline, sim)
		if off && !wasOffline {
			logf("[%s] no signal: buffering", now.Format("15:04"))
		}
		if !off && wasOffline {
			logf("[%s] signal back: sending %d buffered readings", now.Format("15:04"), len(buffer))
		}
		wasOffline = off

		// Readings while the engine runs, plus ONE as it shuts down: a real
		// ELD records the engine shut-down. Without it the last reading
		// before a stop could still be moving (the truck stops between two
		// samples), and the server could not tell when the truck stood
		// still: a 20-minute fuel stop would look like driving.
		if tr.EngineOn || engineWasOn {
			// Simulated time moves in Sample steps and can overshoot the
			// trip's end (the final shut-down) by up to one: that instant is
			// still in the future of the real clock. The trip ends at total.
			at := now
			if sim > total {
				at = simStart.Add(total)
			}
			cfg.Memory.LastSeq++
			pos := tr.Position()
			r := Reading{Seq: cfg.Memory.LastSeq, RecordedAt: at.UTC(), Latitude: &pos.Lat, Longitude: &pos.Lon,
				SpeedMPH: round1(tr.SpeedMPH), OdometerMi: round1(tr.OdometerMi), EngineHours: round1(tr.EngineHours),
				EngineOn: tr.EngineOn}
			buffer = append(buffer, r)
			stats.Readings++
			if stats.FirstRecorded.IsZero() {
				stats.FirstRecorded = r.RecordedAt
			}
			stats.LastRecorded = r.RecordedAt
			cfg.Memory.LastRecordedAt = r.RecordedAt
			stats.MaxBuffered = max(stats.MaxBuffered, len(buffer))
		}
		engineWasOn = tr.EngineOn

		if !off {
			if sim-lastSend >= cfg.SendEvery || tr.Done() {
				if err := flush(); err != nil {
					return &stats, err
				}
				lastSend = sim
				ph := tr.Phase()
				logf("[%s] mile %5.1f/%.1f %3.0f mph %-22s stored %d (+%d duplicates), buffered %d",
					now.Format("15:04"), tr.Mile, tr.Route.Length(), tr.SpeedMPH, ph.Note, stats.Accepted, stats.Duplicates, len(buffer))
			}
			if !tr.EngineOn && sim-lastHeartbeat >= cfg.Heartbeat {
				// Parked: nothing new to say, but "I am alive".
				if err := cfg.Client.Heartbeat(ctx); errors.Is(err, ErrFatal) {
					return &stats, err
				} else if err == nil {
					stats.Heartbeats++
				}
				lastHeartbeat = sim
			}
		}

		if tr.Done() && len(buffer) == 0 {
			// The last Park: the engine is off and everything is sent.
			return &stats, cfg.Memory.remember(tr)
		}

		// Let real time catch up with simulated time.
		sim += cfg.Sample
		tr.Step(cfg.Sample)
		wait := realStart.Add(time.Duration(float64(sim) / speedup)).Sub(cfg.Clock.Now()) // the FITTED speedup
		if err := cfg.Clock.Sleep(ctx, wait); err != nil {
			// Stopped (Ctrl-C): send what we have, keep the memory.
			_ = flush()
			_ = cfg.Memory.remember(tr)
			return &stats, err
		}
	}
}

// remember copies the truck's readings into the memory; writing it to disk
// is the caller's job (it knows the path).
// FitSpeedup returns the highest speedup, up to wanted, that keeps a trip
// of length trip from reaching further back than available.
//
// With speedup N the trip starts trip·(1 − 1/N) before now (see Run), so
//
//	trip·(1 − 1/N) ≤ available   ⇔   N ≤ trip / (trip − available)
//
// A device installed 2 minutes ago cannot replay a 1-hour trip at 60x: its
// readings would claim to be from a truck it was not in yet. Then the trip
// runs near real time instead (or install the device earlier).
func FitSpeedup(trip time.Duration, wanted float64, available time.Duration) float64 {
	if available >= trip {
		return wanted // any speed fits
	}
	if available <= 0 {
		return 1
	}
	return min(wanted, float64(trip)/float64(trip-available))
}

func (m *Memory) remember(t *Truck) error {
	m.OdometerMi, m.EngineHours = t.OdometerMi, t.EngineHours
	return nil
}

func round1(x float64) float64 {
	return float64(int64(x*10+0.5)) / 10
}
