package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

type deviceSvcFixture struct {
	svc     *DeviceService
	devices *memELDRepo
	stints  *memELDAssignments
	trucks  *memTruckRepo
}

func newDeviceSvcFixture() *deviceSvcFixture {
	f := &deviceSvcFixture{devices: newMemELDRepo(), stints: newMemELDAssignments(), trucks: newMemTruckRepo()}
	f.svc = NewDeviceService(f.devices, f.stints, f.trucks)
	return f
}

// withKey adds a device of companyA with a key, and returns the key.
func (f *deviceSvcFixture) withKey(t *testing.T) (*model.ELDDevice, string) {
	t.Helper()
	d := f.devices.add(companyA)
	key, hash, err := auth.NewDeviceKey()
	require.NoError(t, err)
	_, err = f.devices.SetKey(context.Background(), d.ID, hash)
	require.NoError(t, err)
	return d, key
}

func TestDeviceService_Authenticate(t *testing.T) {
	ctx := context.Background()

	t.Run("a valid key: the device, and the key is marked used", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)

		got, err := f.svc.Authenticate(ctx, key)

		require.NoError(t, err)
		assert.Equal(t, auth.Device{DeviceID: d.ID, CompanyID: companyA}, *got)
		assert.Equal(t, 1, f.devices.touched)
	})

	t.Run("junk is refused without a lookup", func(t *testing.T) {
		f := newDeviceSvcFixture()

		_, err := f.svc.Authenticate(ctx, "hello")

		assert.ErrorIs(t, err, model.ErrDeviceKeyInvalid)
	})

	t.Run("a well-formed but unknown key", func(t *testing.T) {
		f := newDeviceSvcFixture()
		key, _, err := auth.NewDeviceKey()
		require.NoError(t, err)

		_, err = f.svc.Authenticate(ctx, key)

		assert.ErrorIs(t, err, model.ErrDeviceKeyInvalid, "the same answer as junk: nothing to learn")
		assert.Zero(t, f.devices.touched)
	})

	t.Run("a retired device", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)
		d.Status = model.ELDRetired

		_, err := f.svc.Authenticate(ctx, key)

		assert.ErrorIs(t, err, model.ErrDeviceRetired)
	})

	t.Run("a faulty device may still talk", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)
		d.Status = model.ELDFaulty

		_, err := f.svc.Authenticate(ctx, key)

		assert.NoError(t, err, "a broken device can still report (diagnostics come in Phase 9)")
	})
}

func TestDeviceService_Me(t *testing.T) {
	ctx := context.Background()
	f := newDeviceSvcFixture()
	d, _ := f.withKey(t)
	dev := auth.Device{DeviceID: d.ID, CompanyID: companyA}

	self, err := f.svc.Me(ctx, dev)
	require.NoError(t, err)
	assert.Equal(t, d.ID, self.Device.ID)
	assert.Nil(t, self.Truck, "not installed")

	tr := f.trucks.add(companyA)
	_, err = f.stints.Open(ctx, model.ELDAssignment{CompanyID: companyA, ELDDeviceID: d.ID, TruckID: tr.ID})
	require.NoError(t, err)

	self, err = f.svc.Me(ctx, dev)
	require.NoError(t, err)
	require.NotNil(t, self.Truck)
	assert.Equal(t, tr.ID, self.Truck.ID)

	_, err = f.svc.Me(ctx, auth.Device{DeviceID: uuid.New()})
	assert.ErrorIs(t, err, model.ErrNotFound)
}
