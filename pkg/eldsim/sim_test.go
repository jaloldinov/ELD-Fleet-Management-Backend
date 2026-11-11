package eldsim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock jumps instead of sleeping.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	if d > 0 {
		c.now = c.now.Add(d)
	}
	return nil
}

// fakeAPI accepts every batch like the real API (seq dedup) and records
// what arrived, and when (by the fake clock).
type fakeAPI struct {
	mu          sync.Mutex
	clock       *fakeClock
	seen        map[int64]bool
	batches     [][]Reading
	sentAtField []time.Time // what the device said its clock was
	sentAt      []time.Time
	heartbeats  int
	failNext    int // answer 503 this many times
	status      int // if set: always answer this
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.Path {
	case "/api/v1/device/heartbeat":
		f.heartbeats++
		w.WriteHeader(http.StatusNoContent)
	case "/api/v1/device/telemetry":
		if f.failNext > 0 {
			f.failNext--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body struct {
			SentAt time.Time `json:"sent_at"`
			Events []Reading
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.sentAtField = append(f.sentAtField, body.SentAt)
		res := SendResult{}
		for _, e := range body.Events {
			if f.seen[e.Seq] {
				res.Duplicates++
			} else {
				f.seen[e.Seq] = true
				res.Accepted++
			}
		}
		f.batches = append(f.batches, body.Events)
		f.sentAt = append(f.sentAt, f.clock.now)
		_ = json.NewEncoder(w).Encode(res)
	}
}

func newSim(t *testing.T, offline []Window) (Config, *fakeAPI, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)}
	api := &fakeAPI{clock: clock, seen: map[int64]bool{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	r, err := BuiltInRoute("dallas-loop")
	require.NoError(t, err)
	mem := &Memory{LastSeq: 100, OdometerMi: 50000, EngineHours: 3000}
	return Config{
		Client: &Client{BaseURL: srv.URL, Key: "eldk_test"}, Truck: NewTruck(r, DefaultTrip(r), mem.OdometerMi, mem.EngineHours),
		Memory: mem, Clock: clock, Speedup: 60, Sample: 10 * time.Second, SendEvery: time.Minute,
		Heartbeat: time.Minute, Offline: offline,
	}, api, clock
}

func TestRun_ATrip(t *testing.T) {
	cfg, api, clock := newSim(t, nil)
	started := clock.Now()

	stats, err := Run(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, stats.Readings, stats.Accepted, "every reading stored once")
	assert.Zero(t, stats.Duplicates)
	assert.Positive(t, stats.Heartbeats, "parked at the fuel stop: heartbeats")
	assert.Equal(t, int64(100+stats.Readings), cfg.Memory.LastSeq, "seq continues from the memory")
	assert.Equal(t, stats.LastRecorded, cfg.Memory.LastRecordedAt, "the next trip must start after it")
	assert.InDelta(t, 50000+cfg.Truck.Route.Length(), cfg.Memory.OdometerMi, 0.01, "the odometer is remembered")

	// The timing rule: readings are never stamped in the future, and the
	// last one is (almost) now.
	for i, b := range api.batches {
		assert.Equal(t, api.sentAt[i], api.sentAtField[i], "sent_at is the device's (real) clock")
		for _, r := range b {
			assert.False(t, r.RecordedAt.After(api.sentAt[i]), "seq %d recorded after it was sent", r.Seq)
		}
	}
	assert.WithinDuration(t, clock.Now(), stats.LastRecorded, time.Minute)
	trip := TripDuration(cfg.Truck.Route, cfg.Truck.Plan)
	assert.InDelta(t, float64(trip)/60, float64(clock.Now().Sub(started)), float64(time.Minute), "60x faster")
}

func TestRun_Offline(t *testing.T) {
	cfg, api, _ := newSim(t, []Window{{From: 10 * time.Minute, To: 15 * time.Minute}})

	stats, err := Run(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, stats.Readings, stats.Accepted, "nothing lost in the tunnel")
	assert.GreaterOrEqual(t, stats.MaxBuffered, 25, "5 minutes of readings every 10s waited in the buffer")
	big := 0
	for _, b := range api.batches {
		big = max(big, len(b))
	}
	assert.GreaterOrEqual(t, big, 25, "sent in one go when the signal came back")
}

func TestRun_ServerTroubleKeepsReadings(t *testing.T) {
	cfg, api, _ := newSim(t, nil)
	api.failNext = 3

	stats, err := Run(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, stats.Readings, stats.Accepted, "503s are temporary: kept and sent later")
}

func TestRun_RevokedKeyStops(t *testing.T) {
	cfg, api, _ := newSim(t, nil)
	api.status = http.StatusUnauthorized

	_, err := Run(context.Background(), cfg)

	assert.ErrorIs(t, err, ErrFatal, "a bad key is not retried forever")
}

func TestParseWindows(t *testing.T) {
	ws, err := ParseWindows("30m-45m, 2h-2h10m")
	require.NoError(t, err)
	assert.Equal(t, []Window{{30 * time.Minute, 45 * time.Minute}, {2 * time.Hour, 2*time.Hour + 10*time.Minute}}, ws)

	for _, bad := range []string{"30m", "45m-30m", "x-y"} {
		_, err := ParseWindows(bad)
		assert.Error(t, err, bad)
	}
	ws, err = ParseWindows("")
	require.NoError(t, err)
	assert.Empty(t, ws)
}

func TestMemory(t *testing.T) {
	path := t.TempDir() + "/dev/mem.json"

	m, err := LoadMemory(path)
	require.NoError(t, err)
	assert.Zero(t, m.LastSeq, "a new device")
	assert.Positive(t, m.OdometerMi, "in a used truck")

	m.LastSeq = 42
	require.NoError(t, m.Save(path))
	again, err := LoadMemory(path)
	require.NoError(t, err)
	assert.Equal(t, int64(42), again.LastSeq)
}

// A device that lost its memory restarts its seq at 1: everything it sends
// is a duplicate of what the API already has. It must say so.
func TestRun_LostMemoryIsReported(t *testing.T) {
	cfg, api, _ := newSim(t, nil)
	for seq := int64(101); seq <= 2000; seq++ {
		api.seen[seq] = true // stored by an earlier run of the same device
	}
	var logs []string
	cfg.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	stats, err := Run(context.Background(), cfg)

	require.NoError(t, err)
	assert.Zero(t, stats.Accepted)
	assert.Equal(t, stats.Readings, stats.Duplicates)
	assert.Condition(t, func() bool {
		for _, l := range logs {
			if strings.Contains(l, "device memory (-state-dir) lost") {
				return true
			}
		}
		return false
	}, "warned")
}

func TestFitSpeedup(t *testing.T) {
	hour := time.Hour
	assert.Equal(t, 60.0, FitSpeedup(hour, 60, 2*hour), "installed long ago: any speed")
	assert.Equal(t, 60.0, FitSpeedup(hour, 60, hour), "exactly enough")
	assert.InDelta(t, 2.0, FitSpeedup(hour, 60, 30*time.Minute), 1e-9, "half an hour back: 2x at most")
	assert.Equal(t, 1.0, FitSpeedup(hour, 60, 0), "installed this instant: real time")
	assert.Equal(t, 1.5, FitSpeedup(hour, 1.5, 59*time.Minute), "never faster than asked")
}

// With NotBefore set, no reading is stamped before it, whatever the
// requested speedup.
func TestRun_NotBeforeInstallation(t *testing.T) {
	cfg, api, clock := newSim(t, nil)
	cfg.NotBefore = clock.Now().Add(-10 * time.Minute) // installed 10 minutes ago
	started := clock.Now()

	stats, err := Run(context.Background(), cfg)

	require.NoError(t, err)
	assert.False(t, stats.FirstRecorded.Before(cfg.NotBefore), "first reading %s, installed %s", stats.FirstRecorded, cfg.NotBefore)
	assert.Less(t, stats.Speedup, cfg.Speedup, "slowed down to fit")
	assert.Greater(t, stats.Speedup, 1.0)
	// Slowed down for real: the pace must follow the fitted speed too, or
	// readings run ahead of the clock (stamped in the future).
	for i, b := range api.batches {
		for _, r := range b {
			assert.False(t, r.RecordedAt.After(api.sentAt[i]), "seq %d recorded %s after it was sent", r.Seq, r.RecordedAt.Sub(api.sentAt[i]))
		}
	}
	trip := TripDuration(cfg.Truck.Route, cfg.Truck.Plan)
	assert.InDelta(t, float64(trip)/stats.Speedup, float64(clock.Now().Sub(started)), float64(time.Minute),
		"the trip took trip/speedup of real time")
}

// Every time the engine goes off (fuel stop, arrival) the device says so
// with one standing reading: without it the server cannot tell when the
// truck stopped, and a parked truck would look like it was still driving.
func TestRun_EngineShutDownIsReported(t *testing.T) {
	cfg, api, _ := newSim(t, nil)

	_, err := Run(context.Background(), cfg)
	require.NoError(t, err)

	var all []Reading
	for _, b := range api.batches {
		all = append(all, b...)
	}
	var offs []Reading
	for i, r := range all {
		if !r.EngineOn {
			offs = append(offs, r)
			assert.Zero(t, r.SpeedMPH, "standing")
			assert.True(t, all[i-1].EngineOn, "one reading per shut-down, not one per sample while parked")
		}
	}
	assert.Len(t, offs, 2, "the fuel stop and the arrival (DefaultTrip)")
	assert.False(t, all[len(all)-1].EngineOn, "the trip ends with the engine off")
}
