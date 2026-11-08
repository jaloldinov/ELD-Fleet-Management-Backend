// Command eldsim pretends to be an ELD device in a truck: it drives a
// route and reports readings to the API, like real hardware would.
//
// Usage:
//
//	ELD_DEVICE_KEY=eldk_... go run ./cmd/eldsim -route dallas-houston -speedup 60 -offline 30m-45m
//
// The key comes from POST /api/v1/eld-devices/{id}/credentials. It is read
// from the environment, never a flag: flags end up in shell history and
// are visible to other users in `ps`.
//
// With -speedup 60, a four-hour trip takes four minutes. Its readings are
// stamped so the trip ENDS now (it "happened" over the last four hours), so
// none is ever in the future.
//
// The device's memory (last seq, odometer, engine hours) is kept in
// -state-dir, so the next run continues where this one stopped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jaloldinov/ELD-learning/pkg/eldsim"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		api       = flag.String("api", envOr("ELD_API", "http://localhost:8080"), "API base URL (or ELD_API)")
		routeName = flag.String("route", "dallas-houston", "built-in route: dallas-houston (~240 mi) or dallas-loop (~20 mi)")
		speedup   = flag.Float64("speedup", 60, "simulated seconds per real second (1 = real time)")
		sample    = flag.Duration("sample", 10*time.Second, "simulated time between readings")
		sendEvery = flag.Duration("send-every", time.Minute, "simulated time between sends while online")
		offlineS  = flag.String("offline", "", "no-signal windows from the trip start, e.g. 30m-45m,2h-2h10m")
		stateDir  = flag.String("state-dir", ".eldsim", "where the device's memory (seq, odometer) is kept")
	)
	flag.Parse()

	key := os.Getenv("ELD_DEVICE_KEY")
	if key == "" {
		return errors.New("set ELD_DEVICE_KEY to the device key (POST /api/v1/eld-devices/{id}/credentials)")
	}
	route, err := eldsim.BuiltInRoute(*routeName)
	if err != nil {
		return err
	}
	windows, err := eldsim.ParseWindows(*offlineS)
	if err != nil {
		return err
	}

	// Ctrl-C stops the trip; buffered readings are still sent, memory kept.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &eldsim.Client{BaseURL: *api, Key: key}
	me, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("who am I: %w", err)
	}
	fmt.Printf("device %s", me.SerialNumber)
	if me.Truck != nil {
		fmt.Printf(" in truck %s\n", me.Truck.UnitNumber)
	} else {
		fmt.Println(" (not installed in any truck: readings will belong to no truck)")
	}

	memPath := filepath.Join(*stateDir, me.DeviceID+".json")
	mem, err := eldsim.LoadMemory(memPath)
	if err != nil {
		return err
	}
	plan := eldsim.DefaultTrip(route)
	trip := eldsim.TripDuration(route, plan)
	// The trip may not reach back before the installation, nor into time
	// the last trip already covered.
	run := *speedup
	notBefore, why := mem.LastRecordedAt, "the last trip ended"
	if me.InstalledAt != nil && me.InstalledAt.After(notBefore) {
		notBefore, why = *me.InstalledAt, "the device was installed"
	}
	if !notBefore.IsZero() {
		run = eldsim.FitSpeedup(trip, *speedup, time.Since(notBefore))
		if run < *speedup {
			fmt.Printf("note: %s only %s ago, so a %s trip cannot be replayed at %gx\n"+
				"      (its first readings would be from before that); running at %.2fx instead.\n",
				why, time.Since(notBefore).Round(time.Second), trip.Round(time.Minute), *speedup, run)
		}
	}
	fmt.Printf("route %s, %.1f miles, trip %s, at %.2fx: about %s; starting at seq %d, odometer %.1f\n",
		route.Name, route.Length(), trip.Round(time.Minute), run,
		(time.Duration(float64(trip) / run)).Round(time.Second), mem.LastSeq+1, mem.OdometerMi)

	stats, runErr := eldsim.Run(ctx, eldsim.Config{
		Client: client, Truck: eldsim.NewTruck(route, plan, mem.OdometerMi, mem.EngineHours), Memory: mem,
		Clock: eldsim.RealClock{}, Speedup: *speedup, Sample: *sample, SendEvery: *sendEvery,
		Heartbeat: time.Minute, Offline: windows, NotBefore: notBefore,
		Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err := mem.Save(memPath); err != nil {
		return errors.Join(runErr, err)
	}
	if stats != nil {
		fmt.Printf("done: %d readings, %d accepted, %d duplicates, %d rejected, %d batches, %d heartbeats, buffer peak %d\n",
			stats.Readings, stats.Accepted, stats.Duplicates, stats.Rejected, stats.Batches, stats.Heartbeats, stats.MaxBuffered)
	}
	if errors.Is(runErr, context.Canceled) {
		return nil // Ctrl-C: a normal way to stop
	}
	return runErr
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
