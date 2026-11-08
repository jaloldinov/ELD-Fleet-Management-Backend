package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// DeviceStore is what DeviceService needs to know about devices
// (repository.ELDDeviceRepository).
type DeviceStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error)
	GetByKeyHash(ctx context.Context, hash []byte) (*model.ELDDevice, error)
	TouchKey(ctx context.Context, id uuid.UUID) error
}

// DeviceStints tells which truck a device is in (repository.ELDAssignmentRepository).
type DeviceStints interface {
	GetOpenByDevice(ctx context.Context, deviceID uuid.UUID) (*model.ELDAssignment, error)
}

// TruckFinder reads a truck (repository.TruckRepository).
type TruckFinder interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Truck, error)
}

// DeviceSelf is what a device learns about itself: its record and the truck
// it is installed in (nil = none).
type DeviceSelf struct {
	Device model.ELDDevice
	Truck  *model.Truck
}

// DeviceService serves the ELD devices themselves (the /device endpoints),
// as opposed to ELDDeviceService, which serves the office managing them.
//
// These endpoints are NOT behind the billing gate: recording hours of
// service is required by law, also for a company whose payment failed.
type DeviceService struct {
	devices DeviceStore
	stints  DeviceStints
	trucks  TruckFinder
}

func NewDeviceService(devices DeviceStore, stints DeviceStints, trucks TruckFinder) *DeviceService {
	return &DeviceService{devices: devices, stints: stints, trucks: trucks}
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
	if err := s.devices.TouchKey(ctx, d.ID); err != nil {
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
	return self, nil
}
