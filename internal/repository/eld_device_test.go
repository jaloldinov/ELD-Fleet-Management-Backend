package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type eldFixture struct {
	devices *ELDDeviceRepository
	trucks  *TruckRepository
	alpha   *model.Company
	bravo   *model.Company
	alphaT1 *model.Truck
	alphaT2 *model.Truck
	bravoT1 *model.Truck
}

func newELDFixture(t *testing.T) *eldFixture {
	t.Helper()
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	f := &eldFixture{
		devices: NewELDDeviceRepository(db),
		trucks:  NewTruckRepository(db),
		alpha:   createCompany(t, companies, "Alpha", "1"),
		bravo:   createCompany(t, companies, "Bravo", "2"),
	}
	ctx := context.Background()
	var err error
	f.alphaT1, err = f.trucks.Create(ctx, newTruck(f.alpha.ID, "101", vin1))
	require.NoError(t, err)
	f.alphaT2, err = f.trucks.Create(ctx, newTruck(f.alpha.ID, "102", vin2))
	require.NoError(t, err)
	f.bravoT1, err = f.trucks.Create(ctx, newTruck(f.bravo.ID, "101", vin3))
	require.NoError(t, err)
	return f
}

func (f *eldFixture) device(t *testing.T, companyID uuid.UUID, serial string) *model.ELDDevice {
	t.Helper()
	d, err := f.devices.Create(context.Background(), model.ELDDevice{
		CompanyID: companyID, SerialNumber: serial, Model: "PT30", FirmwareVersion: "1.2.3",
	})
	require.NoError(t, err)
	return d
}

func TestELDDeviceRepository_CreateAndGet(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()

	created := f.device(t, f.alpha.ID, "PT30-0001")
	assert.True(t, created.IsActive)
	assert.Nil(t, created.TruckID, "new devices are not installed")

	got, err := f.devices.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "PT30-0001", got.SerialNumber)
	assert.Equal(t, "1.2.3", got.FirmwareVersion)
}

func TestELDDeviceRepository_SerialUniqueAcrossCompanies(t *testing.T) {
	f := newELDFixture(t)
	f.device(t, f.alpha.ID, "PT30-0001")

	_, err := f.devices.Create(context.Background(), model.ELDDevice{CompanyID: f.bravo.ID, SerialNumber: "PT30-0001"})

	assert.ErrorIs(t, err, model.ErrSerialTaken)
}

func TestELDDeviceRepository_SetTruck(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d1 := f.device(t, f.alpha.ID, "PT30-0001")
	d2 := f.device(t, f.alpha.ID, "PT30-0002")

	t.Run("install", func(t *testing.T) {
		got, err := f.devices.SetTruck(ctx, d1.ID, &f.alphaT1.ID)
		require.NoError(t, err)
		assert.Equal(t, f.alphaT1.ID, *got.TruckID)
	})

	t.Run("one ELD per truck", func(t *testing.T) {
		_, err := f.devices.SetTruck(ctx, d2.ID, &f.alphaT1.ID)
		assert.ErrorIs(t, err, model.ErrTruckHasELD)
	})

	t.Run("move to another truck frees the first", func(t *testing.T) {
		_, err := f.devices.SetTruck(ctx, d1.ID, &f.alphaT2.ID)
		require.NoError(t, err)
		_, err = f.devices.SetTruck(ctx, d2.ID, &f.alphaT1.ID)
		assert.NoError(t, err)
	})

	t.Run("truck of another company is refused by the database", func(t *testing.T) {
		_, err := f.devices.SetTruck(ctx, d1.ID, &f.bravoT1.ID)
		assert.ErrorContains(t, err, "eld_devices_truck_company_fkey")
	})

	t.Run("uninstall", func(t *testing.T) {
		got, err := f.devices.SetTruck(ctx, d1.ID, nil)
		require.NoError(t, err)
		assert.Nil(t, got.TruckID)
	})

	t.Run("unknown device", func(t *testing.T) {
		_, err := f.devices.SetTruck(ctx, uuid.New(), nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestELDDeviceRepository_List(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	installed := f.device(t, f.alpha.ID, "A-0001")
	_, err := f.devices.SetTruck(ctx, installed.ID, &f.alphaT1.ID)
	require.NoError(t, err)
	inactive := f.device(t, f.alpha.ID, "A-0002")
	inactive.IsActive = false
	_, err = f.devices.Update(ctx, *inactive)
	require.NoError(t, err)
	f.device(t, f.bravo.ID, "B-0001")

	_, total, err := f.devices.List(ctx, model.ELDDeviceFilter{CompanyID: &f.alpha.ID}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)

	active := true
	_, total, err = f.devices.List(ctx, model.ELDDeviceFilter{CompanyID: &f.alpha.ID, IsActive: &active}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)

	inTruck, total, err := f.devices.List(ctx, model.ELDDeviceFilter{TruckID: &f.alphaT1.ID}, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	assert.Equal(t, installed.ID, inTruck[0].ID, `"which device is in truck X?"`)

	_, total, err = f.devices.List(ctx, model.ELDDeviceFilter{}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
}

func TestELDDeviceRepository_UpdateKeepsSerialAndTruck(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d := f.device(t, f.alpha.ID, "PT30-0001")
	_, err := f.devices.SetTruck(ctx, d.ID, &f.alphaT1.ID)
	require.NoError(t, err)

	d.SerialNumber, d.TruckID = "HACKED-1", nil // must be ignored
	d.FirmwareVersion = "2.0.0"
	got, err := f.devices.Update(ctx, *d)

	require.NoError(t, err)
	assert.Equal(t, "2.0.0", got.FirmwareVersion)
	assert.Equal(t, "PT30-0001", got.SerialNumber)
	assert.Equal(t, f.alphaT1.ID, *got.TruckID)
}

func TestELDDeviceRepository_SoftDeleteUninstalls(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d := f.device(t, f.alpha.ID, "PT30-0001")
	_, err := f.devices.SetTruck(ctx, d.ID, &f.alphaT1.ID)
	require.NoError(t, err)

	require.NoError(t, f.devices.SoftDelete(ctx, d.ID))

	_, err = f.devices.GetByID(ctx, d.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, f.devices.SoftDelete(ctx, d.ID), model.ErrNotFound)

	// The truck is free again, and the serial can be registered again.
	other := f.device(t, f.alpha.ID, "PT30-0001")
	_, err = f.devices.SetTruck(ctx, other.ID, &f.alphaT1.ID)
	assert.NoError(t, err)
}

func TestTruckRepository_SoftDeleteUninstallsELD(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d := f.device(t, f.alpha.ID, "PT30-0001")
	_, err := f.devices.SetTruck(ctx, d.ID, &f.alphaT1.ID)
	require.NoError(t, err)

	require.NoError(t, f.trucks.SoftDelete(ctx, f.alphaT1.ID))

	got, err := f.devices.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Nil(t, got.TruckID, "the device came out of the deleted truck")
	assert.True(t, got.IsActive, "and is ready to be installed elsewhere")
}
