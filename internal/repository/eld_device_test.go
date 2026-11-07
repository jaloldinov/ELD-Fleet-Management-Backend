package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type eldFixture struct {
	devices  *ELDDeviceRepository
	installs *ELDAssignmentRepository
	trucks   *TruckRepository
	alpha    *model.Company
	bravo    *model.Company
	alphaT1  *model.Truck
	alphaT2  *model.Truck
	bravoT1  *model.Truck
}

func newELDFixture(t *testing.T) *eldFixture {
	t.Helper()
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	f := &eldFixture{
		devices:  NewELDDeviceRepository(db),
		installs: NewELDAssignmentRepository(db),
		trucks:   NewTruckRepository(db),
		alpha:    createCompany(t, companies, "Alpha", "1"),
		bravo:    createCompany(t, companies, "Bravo", "2"),
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

// install opens a stint: the device is in the truck from now on.
func (f *eldFixture) install(d *model.ELDDevice, truck *model.Truck) (*model.ELDAssignment, error) {
	return f.installs.Open(context.Background(), model.ELDAssignment{
		CompanyID: truck.CompanyID, ELDDeviceID: d.ID, TruckID: truck.ID,
	})
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

func TestELDAssignmentRepository(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d1 := f.device(t, f.alpha.ID, "PT30-0001")
	d2 := f.device(t, f.alpha.ID, "PT30-0002")

	t.Run("install: the device reports its truck", func(t *testing.T) {
		a, err := f.install(d1, f.alphaT1)
		require.NoError(t, err)
		assert.True(t, a.EndedAt == nil)

		got, err := f.devices.GetByID(ctx, d1.ID)
		require.NoError(t, err)
		require.NotNil(t, got.TruckID)
		assert.Equal(t, f.alphaT1.ID, *got.TruckID)

		open, err := f.installs.GetOpenByTruck(ctx, f.alphaT1.ID)
		require.NoError(t, err)
		assert.Equal(t, d1.ID, open.ELDDeviceID)

		installed, err := f.installs.GetInstalled(ctx, f.alphaT1.ID)
		require.NoError(t, err)
		assert.Equal(t, "PT30-0001", installed.SerialNumber)
		_, err = f.installs.GetInstalled(ctx, f.alphaT2.ID)
		assert.ErrorIs(t, err, model.ErrNotFound, "empty truck")
	})

	t.Run("one ELD per truck", func(t *testing.T) {
		_, err := f.install(d2, f.alphaT1)
		assert.ErrorIs(t, err, model.ErrTruckHasELD)
	})

	t.Run("move = end + open; the first truck is free again", func(t *testing.T) {
		ended, err := f.installs.EndOpenByDevice(ctx, d1.ID, nil)
		require.NoError(t, err)
		assert.NotNil(t, ended.EndedAt)
		_, err = f.install(d1, f.alphaT2)
		require.NoError(t, err)
		_, err = f.install(d2, f.alphaT1)
		assert.NoError(t, err)
	})

	t.Run("truck of another company is refused by the database", func(t *testing.T) {
		_, err := f.installs.EndOpenByDevice(ctx, d1.ID, nil)
		require.NoError(t, err)
		// The row claims the device's company; the truck is bravo's.
		_, err = f.installs.Open(ctx, model.ELDAssignment{CompanyID: f.alpha.ID, ELDDeviceID: d1.ID, TruckID: f.bravoT1.ID})
		assert.ErrorContains(t, err, "eta_truck_company_fkey")
	})

	t.Run("end by truck", func(t *testing.T) {
		ended, err := f.installs.EndOpenByTruck(ctx, f.alphaT1.ID, nil)
		require.NoError(t, err)
		assert.Equal(t, d2.ID, ended.ELDDeviceID)

		got, err := f.devices.GetByID(ctx, d2.ID)
		require.NoError(t, err)
		assert.Nil(t, got.TruckID, "not installed any more")

		_, err = f.installs.EndOpenByTruck(ctx, f.alphaT1.ID, nil)
		assert.ErrorIs(t, err, model.ErrNotFound, "empty truck")
		_, err = f.installs.GetOpenByDevice(ctx, d2.ID)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestELDDeviceRepository_List(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	installed := f.device(t, f.alpha.ID, "A-0001")
	_, err := f.install(installed, f.alphaT1)
	require.NoError(t, err)
	inactive := f.device(t, f.alpha.ID, "A-0002")
	inactive.Status = model.ELDFaulty
	updated, err := f.devices.Update(ctx, *inactive)
	require.NoError(t, err)
	assert.False(t, updated.IsActive, "computed by the database from status")
	faulty := model.ELDFaulty
	_, total, err := f.devices.List(ctx, model.ELDDeviceFilter{Status: &faulty}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total, "status filter")
	f.device(t, f.bravo.ID, "B-0001")

	_, total, err = f.devices.List(ctx, model.ELDDeviceFilter{CompanyID: &f.alpha.ID}, 20, 0)
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
	_, err := f.install(d, f.alphaT1)
	require.NoError(t, err)

	d.SerialNumber, d.TruckID = "HACKED-1", nil // must be ignored
	d.FirmwareVersion = "2.0.0"
	got, err := f.devices.Update(ctx, *d)

	require.NoError(t, err)
	assert.Equal(t, "2.0.0", got.FirmwareVersion)
	assert.Equal(t, "PT30-0001", got.SerialNumber)
	assert.Equal(t, f.alphaT1.ID, *got.TruckID)
}

func TestELDDeviceRepository_SoftDelete(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d := f.device(t, f.alpha.ID, "PT30-0001")

	require.NoError(t, f.devices.SoftDelete(ctx, d.ID))

	_, err := f.devices.GetByID(ctx, d.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, f.devices.SoftDelete(ctx, d.ID), model.ErrNotFound)

	// The serial can be registered again.
	f.device(t, f.alpha.ID, "PT30-0001")
}

func TestELDAssignmentRepository_ListHistory(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	d := f.device(t, f.alpha.ID, "PT30-0001")
	first, err := f.install(d, f.alphaT1)
	require.NoError(t, err)
	_, err = f.installs.EndOpenByDevice(ctx, d.ID, nil)
	require.NoError(t, err)
	second, err := f.install(d, f.alphaT2)
	require.NoError(t, err)

	list, total, err := f.installs.ListHistory(ctx, model.ELDAssignmentFilter{ELDDeviceID: &d.ID}, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	assert.Equal(t, second.ID, list[0].Assignment.ID, "newest first")
	assert.Equal(t, first.ID, list[1].Assignment.ID)
	assert.Equal(t, "102", list[0].TruckUnitNumber)
	assert.Equal(t, "PT30-0001", list[0].SerialNumber)

	now := time.Now()
	running, _, err := f.installs.ListHistory(ctx, model.ELDAssignmentFilter{
		TruckID: &f.alphaT2.ID, Period: model.StintPeriod{At: &now}}, 20, 0)
	require.NoError(t, err)
	require.Len(t, running, 1, "the device in truck 102 right now")

	_, total, err = f.installs.ListHistory(ctx, model.ELDAssignmentFilter{CompanyID: &f.bravo.ID}, 20, 0)
	require.NoError(t, err)
	assert.Zero(t, total, "other company")
}
