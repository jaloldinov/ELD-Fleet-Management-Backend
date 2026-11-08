package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// DeviceStore is what DeviceService needs to know about devices
// (repository.ELDDeviceRepository).
type DeviceStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
	GetByKeyHash(ctx context.Context, hash []byte) (*model.ELDDevice, error)
	MarkSeen(ctx context.Context, id uuid.UUID) error
	SetClockOffset(ctx context.Context, id uuid.UUID, offset time.Duration) error
}

// DeviceStints tells which truck a device is in (repository.ELDAssignmentRepository).
type DeviceStints interface {
	GetOpenByDevice(ctx context.Context, deviceID uuid.UUID) (*model.ELDAssignment, error)
}

// TruckFinder reads a truck (repository.TruckRepository).
type TruckFinder interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
}

// TelemetryStore stores device points (repository.TelemetryRepository).
type TelemetryStore interface {
	InsertBatch(ctx context.Context, companyID, deviceID uuid.UUID, points []model.TelemetryPoint) ([]int64, error)
	UpsertState(ctx context.Context, companyID, deviceID uuid.UUID, p model.TelemetryPoint) (bool, error)
	GetState(ctx context.Context, deviceID uuid.UUID) (*model.DeviceState, error)
}

// MaxTelemetryBatch is the most points one request may carry. A device
// that was offline for long sends several batches.
const MaxTelemetryBatch = 500

// Plausibility limits: a reading outside them is a broken sensor or a bug.
const (
	maxSpeedMPH    = 150
	maxOdometerMi  = 10_000_000
	maxEngineHours = 1_000_000
)

// TelemetryInput is one point as the device sent it. Pointers: a missing
// required field is reported for that point, not for the whole batch.
type TelemetryInput struct {
	Seq         *int64
	RecordedAt  *time.Time
	Latitude    *float64
	Longitude   *float64
	SpeedMPH    *float64
	OdometerMi  *float64
	EngineHours *float64
	EngineOn    *bool
}

// TelemetryBatch is one request from a device. SentAt is the device's
// clock when it sent the batch (optional): it lets us measure the clock's
// offset, and no point can have been recorded after it.
type TelemetryBatch struct {
	SentAt *time.Time
	Points []TelemetryInput
}

// RejectedPoint is a point that will never be stored, and why. Index is its
// position in the batch (the seq itself may be what is wrong).
type RejectedPoint struct {
	Index  int
	Seq    *int64
	Reason string
}

// IngestResult gives every point of the batch a FINAL answer: accepted
// (new), duplicate (stored before) or rejected (never will be). The device
// can drop all of them from its buffer; resending changes nothing.
type IngestResult struct {
	Accepted   int
	Duplicates int
	Rejected   []RejectedPoint
}

// DeviceSelf is what a device learns about itself: its record and the truck
// it is installed in (nil = none).
type DeviceSelf struct {
	Device      model.ELDDevice
	Truck       *model.Truck
	InstalledAt *time.Time // when it was installed in Truck (its stint began)
}

// DeviceService serves the ELD devices themselves (the /device endpoints),
// as opposed to ELDDeviceService, which serves the office managing them.
//
// These endpoints are NOT behind the billing gate: recording hours of
// service is required by law, also for a company whose payment failed.
type DeviceService struct {
	now       func() time.Time // our clock; replaceable in tests
	tx        Transactor
	devices   DeviceStore
	stints    DeviceStints
	trucks    TruckFinder
	telemetry TelemetryStore
}

func NewDeviceService(tx Transactor, devices DeviceStore, stints DeviceStints, trucks TruckFinder, telemetry TelemetryStore) *DeviceService {
	return &DeviceService{now: time.Now, tx: tx, devices: devices, stints: stints, trucks: trucks, telemetry: telemetry}
}

// Authenticate turns a device key into the calling device. Every failure
// that says something about keys ("no such key", "revoked", "malformed")
// is the same ErrDeviceKeyInvalid; a retired device is told so
// (ErrDeviceRetired): its key is real, it just may not send any more.
func (s *DeviceService) Authenticate(ctx context.Context, key string) (*auth.Device, error) {
	if !auth.LooksLikeDeviceKey(key) {
		return nil, model.ErrDeviceKeyInvalid // no database work for junk
	}
	d, err := s.devices.GetByKeyHash(ctx, auth.HashDeviceKey(key))
	if errors.Is(err, model.ErrNotFound) {
		return nil, model.ErrDeviceKeyInvalid
	} else if err != nil {
		return nil, err
	}
	if d.Status == model.ELDRetired {
		return nil, model.ErrDeviceRetired
	}
	if err := s.devices.MarkSeen(ctx, d.ID); err != nil {
		return nil, err
	}
	return &auth.Device{DeviceID: d.ID, CompanyID: d.CompanyID}, nil
}

// Me tells the device who it is and which truck it is in.
func (s *DeviceService) Me(ctx context.Context, dev auth.Device) (*DeviceSelf, error) {
	d, err := s.devices.GetByID(ctx, dev.DeviceID)
	if err != nil {
		return nil, err
	}
	self := &DeviceSelf{Device: *d}
	a, err := s.stints.GetOpenByDevice(ctx, d.ID)
	if errors.Is(err, model.ErrNotFound) {
		return self, nil // not installed
	} else if err != nil {
		return nil, err
	}
	if self.Truck, err = s.trucks.GetByID(ctx, a.TruckID); err != nil {
		return nil, err
	}
	self.InstalledAt = &a.StartedAt
	return self, nil
}

// Ingest stores a batch of points from the calling device. Each point is
// judged on its own: one bad reading must not block the good ones, or a
// device would resend the same batch forever and never get past it. The
// result tells the device, for every point, that it is done with it.
//
// Only an unusable request as a whole (empty, too big) is an error.
func (s *DeviceService) Ingest(ctx context.Context, dev auth.Device, batch TelemetryBatch) (*IngestResult, error) {
	in := batch.Points
	switch {
	case len(in) == 0:
		return nil, &validation.ValidationError{Fields: map[string]string{"events": "at least one point is required"}}
	case len(in) > MaxTelemetryBatch:
		return nil, &validation.ValidationError{Fields: map[string]string{
			"events": fmt.Sprintf("at most %d points per request; send the rest in another", MaxTelemetryBatch)}}
	}

	now := s.now()
	res := &IngestResult{}
	reject := func(i int, seq *int64, reason string) {
		res.Rejected = append(res.Rejected, RejectedPoint{Index: i, Seq: seq, Reason: reason})
	}

	// 1. Each point alone: fields, ranges, its time.
	var candidates []indexedPoint
	seen := make(map[int64]bool, len(in))
	for i, raw := range in {
		p, reason := checkPoint(raw)
		if reason == "" {
			reason = checkTime(p.RecordedAt, now, batch.SentAt)
		}
		switch {
		case reason != "":
			reject(i, raw.Seq, reason)
		case seen[p.Seq]:
			res.Duplicates++ // the same seq twice in this batch: keep the first
		default:
			seen[p.Seq] = true
			candidates = append(candidates, indexedPoint{i, p})
		}
	}

	var accepted []int64
	var valid []model.TelemetryPoint
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if batch.SentAt != nil {
			// Measured even when every point is refused: a drifting clock
			// is exactly what the office needs to see then.
			if err := s.devices.SetClockOffset(ctx, dev.DeviceID, batch.SentAt.Sub(now)); err != nil {
				return err
			}
		}

		// 2. Points against each other and against what is stored: seq
		// and time must agree.
		state, err := s.telemetry.GetState(ctx, dev.DeviceID)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return err
		}
		for _, c := range checkOrder(candidates, state) {
			if c.reason != "" {
				reject(c.index, &c.p.Seq, c.reason)
			} else {
				valid = append(valid, c.p)
			}
		}
		if len(valid) == 0 {
			return nil
		}

		// 3. Store them, and the state they lead to: together or not at all.
		if accepted, err = s.telemetry.InsertBatch(ctx, dev.CompanyID, dev.DeviceID, valid); err != nil {
			return err
		}
		latest, ok := newest(valid, accepted)
		if !ok {
			return nil // all duplicates: the state already saw them
		}
		_, err = s.telemetry.UpsertState(ctx, dev.CompanyID, dev.DeviceID, latest)
		return err // an older point than the stored state changes nothing
	})
	if err != nil {
		return nil, err
	}
	res.Accepted = len(accepted)
	res.Duplicates += len(valid) - len(accepted) // stored by an earlier request
	sort.Slice(res.Rejected, func(a, b int) bool { return res.Rejected[a].Index < res.Rejected[b].Index })
	return res, nil
}

type indexedPoint struct {
	index int
	p     model.TelemetryPoint
}

// checkTime: when a reading claims to have happened must be believable.
func checkTime(at, now time.Time, sentAt *time.Time) string {
	switch {
	case at.After(now.Add(model.MaxClockSkew)):
		return "recorded_at: more than 10 minutes in the future (the device clock is wrong: timing malfunction)"
	case at.Before(now.Add(-model.MaxReadingAge)):
		return "recorded_at: older than 30 days"
	case sentAt != nil && at.After(sentAt.Add(time.Second)):
		return "recorded_at: after the batch's sent_at (recorded after it was sent?)"
	}
	return ""
}

type orderVerdict struct {
	indexedPoint
	reason string
}

// checkOrder: a device numbers its readings in the order it records them,
// so a higher seq must not be earlier. A contradiction means the clock
// jumped. The point that contradicts what came before (by seq) is
// refused: we cannot tell which clock reading was the wrong one, but
// accepting both would put two orders into one record.
//
// Checked within the batch, and against the stored state (the device's
// newest reading so far). A late batch with LOWER seqs and earlier times
// (sent after a newer one, e.g. a retry) agrees with both and is fine.
func checkOrder(points []indexedPoint, state *model.DeviceState) []orderVerdict {
	sorted := slices.Clone(points)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].p.Seq < sorted[b].p.Seq })

	out := make([]orderVerdict, 0, len(sorted))
	var prev *model.TelemetryPoint // the latest accepted so far, by seq
	for _, c := range sorted {
		v := orderVerdict{indexedPoint: c}
		switch {
		case prev != nil && c.p.RecordedAt.Before(prev.RecordedAt):
			v.reason = fmt.Sprintf("recorded_at: earlier than seq %d's, which comes before it (the device clock went backwards)", prev.Seq)
		case state != nil && c.p.Seq > state.Seq && c.p.RecordedAt.Before(state.RecordedAt):
			v.reason = fmt.Sprintf("recorded_at: earlier than the stored seq %d's (the device clock went backwards)", state.Seq)
		case state != nil && c.p.Seq < state.Seq && c.p.RecordedAt.After(state.RecordedAt):
			v.reason = fmt.Sprintf("recorded_at: later than the stored seq %d's, which comes after it", state.Seq)
		default:
			p := c.p
			prev = &p
		}
		out = append(out, v)
	}
	return out
}

// newest returns the accepted point with the latest recorded_at (ties: the
// higher seq). ok = false when nothing was accepted.
func newest(points []model.TelemetryPoint, accepted []int64) (model.TelemetryPoint, bool) {
	isNew := make(map[int64]bool, len(accepted))
	for _, seq := range accepted {
		isNew[seq] = true
	}
	var best model.TelemetryPoint
	found := false
	for _, p := range points {
		if !isNew[p.Seq] {
			continue
		}
		if !found || p.RecordedAt.After(best.RecordedAt) || (p.RecordedAt.Equal(best.RecordedAt) && p.Seq > best.Seq) {
			best, found = p, true
		}
	}
	return best, found
}

// checkPoint validates one point; reason "" means it is fine. Time-based
// rules (clock skew, points from the future or too old) come in step 8e.
func checkPoint(in TelemetryInput) (model.TelemetryPoint, string) {
	var v validation.Validator
	v.Check(in.Seq != nil, "seq", "is required")
	v.Check(in.Seq == nil || *in.Seq >= 1, "seq", "must be 1 or more")
	v.Check(in.RecordedAt != nil, "recorded_at", "is required")
	v.Check(in.SpeedMPH != nil, "speed_mph", "is required")
	v.Check(in.OdometerMi != nil, "odometer_mi", "is required")
	v.Check(in.EngineHours != nil, "engine_hours", "is required")
	v.Check(in.EngineOn != nil, "engine_on", "is required")
	v.Check((in.Latitude == nil) == (in.Longitude == nil), "latitude", "latitude and longitude come together (or neither: no GPS fix)")
	v.Check(in.Latitude == nil || between(*in.Latitude, -90, 90), "latitude", "must be between -90 and 90")
	v.Check(in.Longitude == nil || between(*in.Longitude, -180, 180), "longitude", "must be between -180 and 180")
	v.Check(in.SpeedMPH == nil || between(*in.SpeedMPH, 0, maxSpeedMPH), "speed_mph", "must be between 0 and 150")
	v.Check(in.OdometerMi == nil || between(*in.OdometerMi, 0, maxOdometerMi), "odometer_mi", "must be between 0 and 10000000")
	v.Check(in.EngineHours == nil || between(*in.EngineHours, 0, maxEngineHours), "engine_hours", "must be between 0 and 1000000")
	if err := v.Err(); err != nil {
		var vErr *validation.ValidationError
		errors.As(err, &vErr)
		return model.TelemetryPoint{}, reasonOf(vErr.Fields)
	}
	return model.TelemetryPoint{
		Seq: *in.Seq, RecordedAt: *in.RecordedAt, Latitude: in.Latitude, Longitude: in.Longitude,
		SpeedMPH: *in.SpeedMPH, OdometerMi: *in.OdometerMi, EngineHours: *in.EngineHours, EngineOn: *in.EngineOn,
	}, ""
}

// between also refuses NaN: every comparison with NaN is false.
func between(x, lo, hi float64) bool {
	return x >= lo && x <= hi
}

// reasonOf turns field problems into one stable line ("field: problem; ..."),
// sorted, so the same bad point always gets the same reason.
func reasonOf(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+fields[k])
	}
	return strings.Join(parts, "; ")
}
