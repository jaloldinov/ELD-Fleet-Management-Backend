package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

const validVIN = "1M8GDM9AXKP042788" // verified check digit

type memTruckRepo struct {
	trucks  map[uuid.UUID]*model.Truck
	created []model.Truck
	listF   model.TruckFilter
}

func newMemTruckRepo() *memTruckRepo { return &memTruckRepo{trucks: map[uuid.UUID]*model.Truck{}} }

func (m *memTruckRepo) add(companyID uuid.UUID) *model.Truck {
	plate, state := "ABC123", "TX"
	t := &model.Truck{ID: uuid.New(), CompanyID: companyID, UnitNumber: "101", VIN: validVIN,
		LicensePlate: &plate, PlateState: &state, IsActive: true}
	m.trucks[t.ID] = t
	return t
}

func (m *memTruckRepo) Create(_ context.Context, t model.Truck) (*model.Truck, error) {
	m.created = append(m.created, t)
	t.ID = uuid.New()
	t.IsActive = true
	m.trucks[t.ID] = &t
	return &t, nil
}

func (m *memTruckRepo) GetByID(_ context.Context, id uuid.UUID) (*model.Truck, error) {
	t, ok := m.trucks[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (m *memTruckRepo) List(_ context.Context, f model.TruckFilter, _, _ int) ([]model.Truck, int, error) {
	m.listF = f
	return nil, 0, nil
}

func (m *memTruckRepo) Update(_ context.Context, t model.Truck) (*model.Truck, error) {
	m.trucks[t.ID] = &t
	return &t, nil
}

func (m *memTruckRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	delete(m.trucks, id)
	return nil
}

func newTruckSvc() (*TruckService, *memTruckRepo) {
	svc, repo, _ := newAuditedTruckSvc()
	return svc, repo
}

func newAuditedTruckSvc() (*TruckService, *memTruckRepo, *memAuditStore) {
	repo := newMemTruckRepo()
	audit, store := newTestAuditor()
	svc := NewTruckService(repo, fakeCompanies{}, audit)
	svc.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	return svc, repo, store
}

func validTruckInput() CreateTruckInput {
	year := 2022
	return CreateTruckInput{
		UnitNumber: " 101 ", VIN: " 1m8gdm9axkp042788 ", Make: "Freightliner", Model: "Cascadia",
		Year: &year, LicensePlate: " abc 123 ", PlateState: "tx",
	}
}

func TestTruckService_Create_Normalizes(t *testing.T) {
	svc, repo := newTruckSvc()

	got, err := svc.Create(context.Background(), companyAUser, validTruckInput())

	require.NoError(t, err)
	saved := repo.created[0]
	assert.Equal(t, companyA, saved.CompanyID, "company from the token")
	assert.Equal(t, "101", saved.UnitNumber)
	assert.Equal(t, validVIN, saved.VIN, "VIN trimmed and upper-cased")
	assert.Equal(t, "ABC 123", *saved.LicensePlate)
	assert.Equal(t, "TX", *saved.PlateState)
	assert.True(t, got.IsActive)
}

func TestTruckService_Create_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		p      auth.Principal
		modify func(in *CreateTruckInput)
		field  string
	}{
		{"bad check digit", companyAUser, func(in *CreateTruckInput) { in.VIN = "1M8GDM9A1KP042788" }, "vin"},
		{"VIN with O", companyAUser, func(in *CreateTruckInput) { in.VIN = "1M8GDM9AXKP04278O" }, "vin"},
		{"empty unit number", companyAUser, func(in *CreateTruckInput) { in.UnitNumber = " " }, "unit_number"},
		{"unit number with space", companyAUser, func(in *CreateTruckInput) { in.UnitNumber = "TRUCK 1" }, "unit_number"},
		{"year before VINs", companyAUser, func(in *CreateTruckInput) { y := 1975; in.Year = &y }, "year"},
		{"year two years ahead", companyAUser, func(in *CreateTruckInput) { y := 2028; in.Year = &y }, "year"},
		{"plate without state", companyAUser, func(in *CreateTruckInput) { in.PlateState = "" }, "license_plate"},
		{"state without plate", companyAUser, func(in *CreateTruckInput) { in.LicensePlate = "" }, "license_plate"},
		{"unknown plate state", companyAUser, func(in *CreateTruckInput) { in.PlateState = "ZZ" }, "plate_state"},
		{"platform user must pick a company", platformAdmin, func(*CreateTruckInput) {}, "company_id"},
		{"company does not exist", platformAdmin, func(in *CreateTruckInput) { in.CompanyID = &missingCompany }, "company_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTruckSvc()
			in := validTruckInput()
			tt.modify(&in)

			_, err := svc.Create(context.Background(), tt.p, in)

			requireValidationField(t, err, tt.field)
			assert.Empty(t, repo.created)
		})
	}
}

func TestTruckService_Create_NextModelYearAllowed(t *testing.T) {
	svc, _ := newTruckSvc() // "today" is 2026
	in := validTruckInput()
	y := 2027
	in.Year = &y

	_, err := svc.Create(context.Background(), companyAUser, in)

	assert.NoError(t, err, "a 2027 model can be sold in 2026")
}

func TestTruckService_Create_TenantRules(t *testing.T) {
	t.Run("company user cannot create in another company", func(t *testing.T) {
		svc, repo := newTruckSvc()
		in := validTruckInput()
		other := uuid.New()
		in.CompanyID = &other

		_, err := svc.Create(context.Background(), companyAUser, in)

		assert.ErrorIs(t, err, model.ErrNotFound)
		assert.Empty(t, repo.created)
	})

	t.Run("platform user creates in the chosen company", func(t *testing.T) {
		svc, repo := newTruckSvc()
		in := validTruckInput()
		target := uuid.New()
		in.CompanyID = &target

		_, err := svc.Create(context.Background(), platformAdmin, in)

		require.NoError(t, err)
		assert.Equal(t, target, repo.created[0].CompanyID)
	})
}

func TestTruckService_Scope(t *testing.T) {
	svc, repo := newTruckSvc()
	other := repo.add(uuid.New())
	own := repo.add(companyA)

	_, err := svc.Get(context.Background(), companyAUser, other.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = svc.Update(context.Background(), companyAUser, other.ID, UpdateTruckInput{Make: ptr("X")})
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(context.Background(), companyAUser, other.ID), model.ErrNotFound)
	assert.Contains(t, repo.trucks, other.ID)

	_, err = svc.Get(context.Background(), companyAUser, own.ID)
	assert.NoError(t, err)

	active := true
	_, err = svc.List(context.Background(), companyAUser, TruckListParams{CompanyID: &other.CompanyID, IsActive: &active})
	require.NoError(t, err)
	assert.Equal(t, &companyA, repo.listF.CompanyID, "forced to own company")
	assert.Equal(t, &active, repo.listF.IsActive, "other filters are kept")
}

func TestTruckService_Update(t *testing.T) {
	svc, repo := newTruckSvc()
	tr := repo.add(companyA)

	t.Run("deactivate (company users may)", func(t *testing.T) {
		got, err := svc.Update(context.Background(), companyAUser, tr.ID, UpdateTruckInput{IsActive: ptr(false)})
		require.NoError(t, err)
		assert.False(t, got.IsActive)
		assert.Equal(t, "TX", *got.PlateState, "untouched fields kept")
	})

	t.Run("remove plate: both empty", func(t *testing.T) {
		got, err := svc.Update(context.Background(), companyAUser, tr.ID, UpdateTruckInput{
			LicensePlate: ptr(""), PlateState: ptr(""),
		})
		require.NoError(t, err)
		assert.Nil(t, got.LicensePlate)
		assert.Nil(t, got.PlateState)
	})

	t.Run("changing only the plate keeps the state", func(t *testing.T) {
		tr2 := repo.add(companyA)
		got, err := svc.Update(context.Background(), companyAUser, tr2.ID, UpdateTruckInput{LicensePlate: ptr("new1")})
		require.NoError(t, err)
		assert.Equal(t, "NEW1", *got.LicensePlate)
		assert.Equal(t, "TX", *got.PlateState)
	})

	t.Run("removing only the plate is invalid", func(t *testing.T) {
		tr3 := repo.add(companyA)
		_, err := svc.Update(context.Background(), companyAUser, tr3.ID, UpdateTruckInput{LicensePlate: ptr("")})
		requireValidationField(t, err, "license_plate")
	})

	t.Run("empty body", func(t *testing.T) {
		_, err := svc.Update(context.Background(), companyAUser, tr.ID, UpdateTruckInput{})
		requireValidationField(t, err, "body")
	})
}

// ---------- audit ----------

func TestTruckService_AuditCreateUpdateDelete(t *testing.T) {
	svc, _, store := newAuditedTruckSvc()
	p := auth.Principal{UserID: uuid.New(), CompanyID: &companyA}

	created, err := svc.Create(context.Background(), p, validTruckInput())
	require.NoError(t, err)
	e := store.last(t)
	assert.Equal(t, "truck.create", e.Action)
	assert.Equal(t, model.AuditTruck, e.Entity)
	assert.Equal(t, created.ID.String(), e.EntityID)
	assert.Equal(t, &companyA, e.CompanyID)
	assert.Equal(t, model.AuditChange{Old: "", New: "101"}, e.Changes["unit_number"])

	_, err = svc.Update(context.Background(), p, created.ID, UpdateTruckInput{UnitNumber: ptr("101A"), Make: ptr(created.Make)})
	require.NoError(t, err)
	assert.Equal(t, model.AuditChanges{"unit_number": {Old: "101", New: "101A"}}, store.last(t).Changes,
		"only what really changed (make was sent unchanged)")

	require.NoError(t, svc.Delete(context.Background(), p, created.ID))
	assert.Equal(t, []string{"truck.create", "truck.update", "truck.delete"}, store.actions())
	store.requireAllInTx(t)
}

func TestTruckService_AuditSkipsNoOpUpdate(t *testing.T) {
	svc, repo, store := newAuditedTruckSvc()
	tr := repo.add(companyA)

	_, err := svc.Update(context.Background(), platformAdmin, tr.ID, UpdateTruckInput{UnitNumber: ptr(tr.UnitNumber)})

	require.NoError(t, err)
	assert.Empty(t, store.entries, "nothing changed, nothing recorded")
}

func TestTruckService_NoAuditWhenRefused(t *testing.T) {
	svc, repo, store := newAuditedTruckSvc()
	tr := repo.add(uuid.New()) // another company

	_, err := svc.Update(context.Background(), companyAUser, tr.ID, UpdateTruckInput{UnitNumber: ptr("X")})

	require.Error(t, err)
	assert.Empty(t, store.entries)
}

func TestTruckService_TellsBillingWhenTheFleetChanges(t *testing.T) {
	svc, repo := newTruckSvc()
	var told []uuid.UUID
	svc.OnFleetChange(func(_ context.Context, companyID uuid.UUID) { told = append(told, companyID) })
	ctx := context.Background()

	created, err := svc.Create(ctx, companyAUser, validTruckInput())
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{companyA}, told, "a new active truck")

	_, err = svc.Update(ctx, companyAUser, created.ID, UpdateTruckInput{Make: ptr("Volvo")})
	require.NoError(t, err)
	assert.Len(t, told, 1, "a rename does not change the count")

	_, err = svc.Update(ctx, companyAUser, created.ID, UpdateTruckInput{IsActive: ptr(false)})
	require.NoError(t, err)
	assert.Len(t, told, 2, "deactivated: one truck fewer to bill")

	parked := repo.add(companyA)
	parked.IsActive = false
	require.NoError(t, svc.Delete(ctx, companyAUser, parked.ID))
	assert.Len(t, told, 2, "deleting an inactive truck changes nothing billed")

	require.NoError(t, svc.Delete(ctx, companyAUser, repo.add(companyA).ID))
	assert.Len(t, told, 3)

	_, err = svc.Create(ctx, companyAUser, CreateTruckInput{UnitNumber: "bad unit!", VIN: validVIN})
	require.Error(t, err)
	assert.Len(t, told, 3, "a failed change tells nobody")
}
