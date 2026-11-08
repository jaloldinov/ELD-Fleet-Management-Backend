package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

type memELDRepo struct {
	devices map[uuid.UUID]*model.ELDDevice
	keys    map[string]uuid.UUID // hex(hash) → device
	touched int
	created []model.ELDDevice
	listF   model.ELDDeviceFilter
	events  []string // "lock", and "release" from fakeReleaser: their order
}

func newMemELDRepo() *memELDRepo {
	return &memELDRepo{devices: map[uuid.UUID]*model.ELDDevice{}, keys: map[string]uuid.UUID{}}
}

func (m *memELDRepo) SetKey(_ context.Context, id uuid.UUID, hash []byte) (*model.ELDDevice, error) {
	d, ok := m.devices[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	for k, owner := range m.keys {
		if owner == id {
			delete(m.keys, k) // the old key stops working
		}
	}
	d.KeyCreatedAt = nil
	if hash != nil {
		m.keys[hex.EncodeToString(hash)] = id
		now := time.Now()
		d.KeyCreatedAt = &now
	}
	c := *d
	return &c, nil
}

func (m *memELDRepo) GetByKeyHash(ctx context.Context, hash []byte) (*model.ELDDevice, error) {
	id, ok := m.keys[hex.EncodeToString(hash)]
	if !ok {
		return nil, model.ErrNotFound
	}
	return m.GetByID(ctx, id)
}

func (m *memELDRepo) SetClockOffset(_ context.Context, id uuid.UUID, offset time.Duration) error {
	if d, ok := m.devices[id]; ok {
		d.ClockOffset = &offset
	}
	return nil
}

func (m *memELDRepo) MarkSeen(context.Context, uuid.UUID) error {
	m.touched++
	return nil
}

func (m *memELDRepo) add(companyID uuid.UUID) *model.ELDDevice {
	d := &model.ELDDevice{ID: uuid.New(), CompanyID: companyID, SerialNumber: "PT30-0001", Status: model.ELDActive, IsActive: true}
	m.devices[d.ID] = d
	return d
}

func (m *memELDRepo) Create(_ context.Context, d model.ELDDevice) (*model.ELDDevice, error) {
	m.created = append(m.created, d)
	d.ID, d.Status, d.IsActive = uuid.New(), model.ELDActive, true
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
	d.IsActive = d.Status == model.ELDActive // like the generated column
	m.devices[d.ID] = &d
	return &d, nil
}

func (m *memELDRepo) LockByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside transaction")
	}
	m.events = append(m.events, "lock")
	return m.GetByID(ctx, id)
}

func (m *memELDRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	delete(m.devices, id)
	return nil
}

// memStates: last known states by device.
type memStates map[uuid.UUID]*model.DeviceState

func (m memStates) GetState(_ context.Context, id uuid.UUID) (*model.DeviceState, error) {
	st, ok := m[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	return st, nil
}

type eldSvcFixture struct {
	svc      *ELDDeviceService
	states   memStates
	repo     *memELDRepo
	releaser *fakeReleaser
	audit    *memAuditStore
}

func newELDSvcFixture() *eldSvcFixture {
	f := &eldSvcFixture{repo: newMemELDRepo()}
	f.releaser = &fakeReleaser{log: &f.repo.events}
	var audit *Auditor
	audit, f.audit = newTestAuditor()
	f.states = memStates{}
	f.svc = NewELDDeviceService(f.repo, f.releaser, f.states, fakeCompanies{}, audit)
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
	assert.ErrorIs(t, f.svc.Delete(ctx, companyAUser, other.ID), model.ErrNotFound)
	assert.Contains(t, f.repo.devices, other.ID, "not deleted")

	_, err = f.svc.List(ctx, companyAUser, ELDDeviceListParams{CompanyID: &companyB})
	require.NoError(t, err)
	assert.Equal(t, &companyA, f.repo.listF.CompanyID, "forced to own company")

	_, err = f.svc.Get(ctx, platformAdmin, other.ID)
	assert.NoError(t, err, "platform users see all devices")
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

func TestELDDeviceService_DeleteTakesItOutOfItsTruck(t *testing.T) {
	f := newELDSvcFixture()
	d := f.repo.add(companyA)

	require.NoError(t, f.svc.Delete(context.Background(), companyAUser, d.ID))

	assert.Equal(t, []uuid.UUID{d.ID}, f.releaser.devices)
	assert.Equal(t, []string{"lock", "release"}, f.repo.events, "lock first, or an install could slip in")
	assert.True(t, f.releaser.allInTx)
}

func TestELDDeviceService_Status(t *testing.T) {
	ctx := context.Background()

	t.Run("a faulty device may stay in its truck", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		truckID := uuid.New()
		d.TruckID = &truckID

		got, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{Status: ptr(model.ELDFaulty)})

		require.NoError(t, err)
		assert.Equal(t, model.ELDFaulty, got.Status)
		assert.False(t, got.IsActive)
	})

	t.Run("a retired one must come out first", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		truckID := uuid.New()
		d.TruckID = &truckID

		_, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{Status: ptr(model.ELDRetired)})

		requireValidationField(t, err, "status")
	})

	t.Run("is_active false still means retired", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)

		got, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{IsActive: ptr(false)})

		require.NoError(t, err)
		assert.Equal(t, model.ELDRetired, got.Status)
	})

	t.Run("status and is_active must agree", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)

		_, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{Status: ptr(model.ELDFaulty), IsActive: ptr(true)})

		requireValidationField(t, err, "is_active")
	})

	t.Run("unknown status", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)

		_, err := f.svc.Update(ctx, companyAUser, d.ID, UpdateELDDeviceInput{Status: ptr(model.ELDStatus("lost"))})

		requireValidationField(t, err, "status")
	})
}

// ---------- device keys ----------

func TestELDDeviceService_IssueKey(t *testing.T) {
	ctx := context.Background()

	t.Run("issue, then rotate", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)

		first, got, err := f.svc.IssueKey(ctx, companyAUser, d.ID)
		require.NoError(t, err)
		assert.True(t, auth.LooksLikeDeviceKey(first))
		assert.NotNil(t, got.KeyCreatedAt)
		found, err := f.repo.GetByKeyHash(ctx, auth.HashDeviceKey(first))
		require.NoError(t, err, "only the hash is stored, and it finds the device")
		assert.Equal(t, d.ID, found.ID)

		second, _, err := f.svc.IssueKey(ctx, companyAUser, d.ID)
		require.NoError(t, err)
		assert.NotEqual(t, first, second)
		_, err = f.repo.GetByKeyHash(ctx, auth.HashDeviceKey(first))
		assert.ErrorIs(t, err, model.ErrNotFound, "rotating stops the old key at once")

		assert.Equal(t, []string{"eld_device.issue_key", "eld_device.rotate_key"}, f.audit.actions())
		for _, e := range f.audit.entries {
			assert.Empty(t, e.Changes, "never the key (or its hash) in the audit log")
		}
		f.audit.requireAllInTx(t)
	})

	t.Run("a retired device gets no key", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(companyA)
		d.Status = model.ELDRetired

		_, _, err := f.svc.IssueKey(ctx, companyAUser, d.ID)

		requireValidationField(t, err, "status")
	})

	t.Run("another company's device does not exist", func(t *testing.T) {
		f := newELDSvcFixture()
		d := f.repo.add(uuid.New())

		_, _, err := f.svc.IssueKey(ctx, companyAUser, d.ID)

		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestELDDeviceService_RevokeKey(t *testing.T) {
	ctx := context.Background()
	f := newELDSvcFixture()
	d := f.repo.add(companyA)
	key, _, err := f.svc.IssueKey(ctx, companyAUser, d.ID)
	require.NoError(t, err)

	got, err := f.svc.RevokeKey(ctx, companyAUser, d.ID)

	require.NoError(t, err)
	assert.Nil(t, got.KeyCreatedAt)
	_, err = f.repo.GetByKeyHash(ctx, auth.HashDeviceKey(key))
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Equal(t, "eld_device.revoke_key", f.audit.last(t).Action)

	_, err = f.svc.RevokeKey(ctx, companyAUser, d.ID)
	require.NoError(t, err, "no key: nothing to revoke")
	assert.Len(t, f.audit.entries, 2, "and nothing recorded")
}

func TestELDDeviceService_State(t *testing.T) {
	ctx := context.Background()
	f := newELDSvcFixture()
	d := f.repo.add(companyA)

	got, st, err := f.svc.State(ctx, companyAUser, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.ID, got.ID)
	assert.Nil(t, st, "never sent a reading: not an error")

	f.states[d.ID] = &model.DeviceState{ELDDeviceID: d.ID, Moving: true}
	_, st, err = f.svc.State(ctx, companyAUser, d.ID)
	require.NoError(t, err)
	assert.True(t, st.Moving)

	other := f.repo.add(uuid.New())
	_, _, err = f.svc.State(ctx, companyAUser, other.ID)
	assert.ErrorIs(t, err, model.ErrNotFound, "another company's device")
}
