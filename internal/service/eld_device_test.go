package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type memELDRepo struct {
	devices  map[uuid.UUID]*model.ELDDevice
	created  []model.ELDDevice
	listF    model.ELDDeviceFilter
	setCalls int
}

func newMemELDRepo() *memELDRepo { return &memELDRepo{devices: map[uuid.UUID]*model.ELDDevice{}} }

func (m *memELDRepo) add(companyID uuid.UUID) *model.ELDDevice {
	d := &model.ELDDevice{ID: uuid.New(), CompanyID: companyID, SerialNumber: "PT30-0001", IsActive: true}
	m.devices[d.ID] = d
	return d
}

func (m *memELDRepo) Create(_ context.Context, d model.ELDDevice) (*model.ELDDevice, error) {
	m.created = append(m.created, d)
	d.ID, d.IsActive = uuid.New(), true
	m.devices[d.ID] = &d
	return &d, nil
}

func (m *memELDRepo) GetByID(_ context.Context, id uuid.UUID) (*model.ELDDevice, error) {
	d, ok := m.devices[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (m *memELDRepo) List(_ context.Context, f model.ELDDeviceFilter, _, _ int) ([]model.ELDDevice, int, error) {
	m.listF = f
	return nil, 0, nil
}

func (m *memELDRepo) Update(_ context.Context, d model.ELDDevice) (*model.ELDDevice, error) {
	m.devices[d.ID] = &d
	return &d, nil
}

func (m *memELDRepo) SetTruck(_ context.Context, id uuid.UUID, truckID *uuid.UUID) (*model.ELDDevice, error) {
	m.setCalls++
	m.devices[id].TruckID = truckID
	c := *m.devices[id]
	return &c, nil
}

func (m *memELDRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	delete(m.devices, id)
	return nil
}

type eldSvcFixture struct {
	svc    *ELDDeviceService
	repo   *memELDRepo
	trucks *memTruckRepo
	audit  *memAuditStore
}

func newELDSvcFixture() *eldSvcFixture {
	f := &eldSvcFixture{repo: newMemELDRepo(), trucks: newMemTruckRepo()}
	var audit *Auditor
	audit, f.audit = newTestAuditor()
	f.svc = NewELDDeviceService(f.repo, f.trucks, fakeCompanies{}, audit)
	return f
}

func TestELDDeviceService_Create_Normalizes(t *testing.T) {
	f := newELDSvcFixture()

	_, err := f.svc.Create(context.Background(), companyAUser, CreateELDDeviceInput{
		SerialNumber: " pt30-0001 ", Model: " PT30 ", FirmwareVersion: " 1.2.3 ",
	})

	require.NoError(t, err)
	saved := f.repo.created[0]
	assert.Equal(t, companyA, saved.CompanyID, "company from the token")
	assert.Equal(t, "PT30-0001", saved.SerialNumber, "trimmed and upper-cased")
	assert.Equal(t, "PT30", saved.Model)
	assert.Equal(t, "1.2.3", saved.FirmwareVersion)
}

func TestELDDeviceService_Create_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		in    CreateELDDeviceInput
		field string
	}{
		{"serial too short", CreateELDDeviceInput{SerialNumber: "AB1"}, "serial_number"},
		{"serial bad chars", CreateELDDeviceInput{SerialNumber: "PT30/0001"}, "serial_number"},
		{"firmware too long", CreateELDDeviceInput{SerialNumber: "PT30-0001", FirmwareVersion: string(make([]byte, 51))}, "firmware_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newELDSvcFixture()
			_, err := f.svc.Create(context.Background(), companyAUser, tt.in)
			requireValidationField(t, err, tt.field)
			assert.Empty(t, f.repo.created)
		})
	}
}

func TestELDDeviceService_Create_PlatformAdminNeedsCompany(t *testing.T) {
	f := newELDSvcFixture()

	_, err := f.svc.Create(context.Background(), platformAdmin, CreateELDDeviceInput{SerialNumber: "PT30-0001"})

	requireValidationField(t, err, "company_id")
}

func TestELDDeviceService_Scope(t *testing.T) {
	f := newELDSvcFixture()
	companyB := uuid.New()
	other := f.repo.add(companyB)
	ctx := context.Background()

	_, err := f.svc.Get(ctx, companyAUser, other.ID)
	assert.ErrorIs(t, err, model.ErrNotFound, "other tenant's device: 404")
	_, err = f.svc.Update(ctx, companyAUser, other.ID, UpdateELDDeviceInput{Model: ptr("X")})
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = f.svc.UnassignTruck(ctx, companyAUser, other.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, f.svc.Delete(ctx, companyAUser, other.ID), model.ErrNotFound)
	assert.Contains(t, f.repo.devices, other.ID, "not deleted")

	_, err = f.svc.List(ctx, companyAUser, ELDDeviceListParams{CompanyID: &companyB})
	require.NoError(t, err)
	assert.Equal(t, &companyA, f.repo.listF.CompanyID, "forced to own company")

	_, err = f.svc.Get(ctx, platformAdmin, other.ID)
	assert.NoError(t, err, "platform users see all devices")
}

func TestELDDeviceService_AssignTruck(t *testing.T) {
	ctx := context.Background()

	t.Run("installs in a truck of the same company", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		tr := f.trucks.add(companyA)

		got, err := f.svc.AssignTruck(ctx, companyAUser, d.ID, tr.ID)

		require.NoError(t, err)
		assert.Equal(t, tr.ID, *got.TruckID)
	})

	t.Run("other company's truck looks like a missing one", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		foreign := f.trucks.add(uuid.New())

		_, errForeign := f.svc.AssignTruck(ctx, companyAUser, d.ID, foreign.ID)
		_, errMissing := f.svc.AssignTruck(ctx, companyAUser, d.ID, uuid.New())

		requireValidationField(t, errForeign, "truck_id")
		assert.Equal(t, errMissing.Error(), errForeign.Error(), "no way to probe other tenants' truck IDs")
		assert.Zero(t, f.repo.setCalls)
	})

	t.Run("platform admin cannot cross companies either", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		foreign := f.trucks.add(uuid.New())

		_, err := f.svc.AssignTruck(ctx, platformAdmin, d.ID, foreign.ID)

		requireValidationField(t, err, "truck_id")
	})

	t.Run("out-of-service truck", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		tr := f.trucks.add(companyA)
		tr.IsActive = false

		_, err := f.svc.AssignTruck(ctx, companyAUser, d.ID, tr.ID)

		requireValidationField(t, err, "truck_id")
	})

	t.Run("inactive device", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		d.IsActive = false
		tr := f.trucks.add(companyA)

		_, err := f.svc.AssignTruck(ctx, companyAUser, d.ID, tr.ID)

		requireValidationField(t, err, "is_active")
	})
}

func TestELDDeviceService_UnassignTruck(t *testing.T) {
	f := newELDSvcFixture()
	d := f.repo.add(companyA)
	truckID := uuid.New()
	d.TruckID = &truckID

	got, err := f.svc.UnassignTruck(context.Background(), companyAUser, d.ID)

	require.NoError(t, err)
	assert.Nil(t, got.TruckID)
}

func TestELDDeviceService_Update(t *testing.T) {
	ctx := context.Background()

	t.Run("changes firmware", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)

		got, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{FirmwareVersion: ptr(" 2.0.0 ")})

		require.NoError(t, err)
		assert.Equal(t, "2.0.0", got.FirmwareVersion)
		assert.Equal(t, "PT30-0001", got.SerialNumber, "unchanged")
	})

	t.Run("empty body", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		_, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{})
		requireValidationField(t, err, "body")
	})

	t.Run("cannot deactivate while installed", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		truckID := uuid.New()
		d.TruckID = &truckID

		_, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{IsActive: ptr(false)})

		requireValidationField(t, err, "is_active")
		assert.True(t, f.repo.devices[d.ID].IsActive, "not saved")
	})
}

// ---------- audit ----------

func TestELDDeviceService_AuditTruckAssignment(t *testing.T) {
	f := newELDSvcFixture()
	d := f.repo.add(companyA)
	first, second := f.trucks.add(companyA), f.trucks.add(companyA)

	_, err := f.svc.AssignTruck(context.Background(), companyAUser, d.ID, first.ID)
	require.NoError(t, err)
	_, err = f.svc.AssignTruck(context.Background(), companyAUser, d.ID, second.ID)
	require.NoError(t, err)
	_, err = f.svc.UnassignTruck(context.Background(), companyAUser, d.ID)
	require.NoError(t, err)

	require.Equal(t, []string{"eld_device.assign_truck", "eld_device.assign_truck", "eld_device.unassign_truck"}, f.audit.actions())
	assert.Equal(t, model.AuditChange{Old: nil, New: first.ID}, f.audit.entries[0].Changes["truck_id"])
	assert.Equal(t, model.AuditChange{Old: first.ID, New: second.ID}, f.audit.entries[1].Changes["truck_id"], "moved between trucks")
	assert.Equal(t, model.AuditChange{Old: second.ID, New: nil}, f.audit.entries[2].Changes["truck_id"])
	assert.Equal(t, &companyA, f.audit.entries[0].CompanyID)
	f.audit.requireAllInTx(t)
}

func TestELDDeviceService_AuditSkipsNoOpUnassign(t *testing.T) {
	f := newELDSvcFixture()
	d := f.repo.add(companyA) // not installed

	_, err := f.svc.UnassignTruck(context.Background(), companyAUser, d.ID)

	require.NoError(t, err)
	assert.Empty(t, f.audit.entries)
}

func TestELDDeviceService_AuditCreateUpdateDelete(t *testing.T) {
	f := newELDSvcFixture()

	d, err := f.svc.Create(context.Background(), companyAUser, CreateELDDeviceInput{SerialNumber: "pt30-0002", Model: "PT30"})
	require.NoError(t, err)
	_, err = f.svc.Update(context.Background(), companyAUser, d.ID, UpdateELDDeviceInput{FirmwareVersion: ptr("2.1")})
	require.NoError(t, err)
	require.NoError(t, f.svc.Delete(context.Background(), companyAUser, d.ID))

	assert.Equal(t, []string{"eld_device.create", "eld_device.update", "eld_device.delete"}, f.audit.actions())
	assert.Equal(t, "PT30-0002", f.audit.entries[0].Changes["serial_number"].New)
	assert.Equal(t, model.AuditChanges{"firmware_version": {Old: "", New: "2.1"}}, f.audit.entries[1].Changes)
	f.audit.requireAllInTx(t)
}
