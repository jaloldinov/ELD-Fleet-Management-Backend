package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Verified valid VINs (see internal/vin tests).
const (
	vin1 = "1M8GDM9AXKP042788"
	vin2 = "1HGCM82633A004352"
	vin3 = "11111111111111111"
)

func newTruck(companyID uuid.UUID, unit, vin string) model.Truck {
	year := 2022
	plate, state := "ABC1234", "TX"
	return model.Truck{
		CompanyID: companyID, UnitNumber: unit, VIN: vin,
		Make: "Freightliner", Model: "Cascadia", Year: &year,
		LicensePlate: &plate, PlateState: &state,
	}
}

func TestTruckRepository_CreateAndGet(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewTruckRepository(db)
	ctx := context.Background()

	created, err := repo.Create(ctx, newTruck(c.ID, "101", vin1))
	require.NoError(t, err)
	assert.True(t, created.IsActive)
	require.NotNil(t, created.Year)
	assert.Equal(t, 2022, *created.Year, "SMALLINT round-trips into an int")

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, vin1, got.VIN)
	assert.Equal(t, "TX", *got.PlateState)
}

func TestTruckRepository_Uniqueness(t *testing.T) {
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	alpha := createCompany(t, companies, "Alpha", "1")
	bravo := createCompany(t, companies, "Bravo", "2")
	repo := NewTruckRepository(db)
	ctx := context.Background()
	_, err := repo.Create(ctx, newTruck(alpha.ID, "101", vin1))
	require.NoError(t, err)

	t.Run("unit number unique within a company (case-insensitive)", func(t *testing.T) {
		_, err := repo.Create(ctx, newTruck(alpha.ID, "101", vin2))
		assert.ErrorIs(t, err, model.ErrUnitNumberTaken)
	})

	t.Run("same unit number in another company is fine", func(t *testing.T) {
		_, err := repo.Create(ctx, newTruck(bravo.ID, "101", vin2))
		assert.NoError(t, err)
	})

	t.Run("VIN unique across ALL companies", func(t *testing.T) {
		_, err := repo.Create(ctx, newTruck(bravo.ID, "202", vin1))
		assert.ErrorIs(t, err, model.ErrVINTaken)
	})
}

func TestTruckRepository_PlateNeedsState(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	tr := newTruck(c.ID, "101", vin1)
	tr.PlateState = nil // plate without state

	_, err := NewTruckRepository(db).Create(context.Background(), tr)

	assert.ErrorContains(t, err, "trucks_plate_pair", "database backstop for the service rule")
}

func TestTruckRepository_ListFilters(t *testing.T) {
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	alpha := createCompany(t, companies, "Alpha", "1")
	bravo := createCompany(t, companies, "Bravo", "2")
	repo := NewTruckRepository(db)
	ctx := context.Background()
	a1, err := repo.Create(ctx, newTruck(alpha.ID, "101", vin1))
	require.NoError(t, err)
	_, err = repo.Create(ctx, newTruck(alpha.ID, "102", vin2))
	require.NoError(t, err)
	_, err = repo.Create(ctx, newTruck(bravo.ID, "101", vin3))
	require.NoError(t, err)

	a1.IsActive = false
	_, err = repo.Update(ctx, *a1)
	require.NoError(t, err)

	_, total, err := repo.List(ctx, model.TruckFilter{CompanyID: &alpha.ID}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)

	active := true
	items, total, err := repo.List(ctx, model.TruckFilter{CompanyID: &alpha.ID, IsActive: &active}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, "102", items[0].UnitNumber)

	_, total, err = repo.List(ctx, model.TruckFilter{}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
}

func TestTruckRepository_UpdateAndDelete(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewTruckRepository(db)
	ctx := context.Background()
	tr, err := repo.Create(ctx, newTruck(c.ID, "101", vin1))
	require.NoError(t, err)

	tr.UnitNumber, tr.LicensePlate, tr.PlateState = "101A", nil, nil
	tr.VIN = vin2 // must be ignored: the VIN is immutable
	got, err := repo.Update(ctx, *tr)
	require.NoError(t, err)
	assert.Equal(t, "101A", got.UnitNumber)
	assert.Nil(t, got.LicensePlate)
	assert.Equal(t, vin1, got.VIN, "Update never changes the VIN")

	require.NoError(t, repo.SoftDelete(ctx, tr.ID))
	_, err = repo.GetByID(ctx, tr.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	_, err = repo.Create(ctx, newTruck(c.ID, "101A", vin1))
	assert.NoError(t, err, "VIN and unit number are free again after soft delete")
}
