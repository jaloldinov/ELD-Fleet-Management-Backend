package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// fakeCompanyRepo is a hand-written stub. Each test sets only the functions it
// expects to be called; calling any other method fails the test. That also
// proves things like "invalid input never reaches the database".
type fakeCompanyRepo struct {
	t         *testing.T
	createFn  func(c model.Company) (*model.Company, error)
	getByIDFn func(id uuid.UUID) (*model.Company, error)
	listFn    func(f model.CompanyFilter, limit, offset int) ([]model.Company, int, error)
	updateFn  func(c model.Company) (*model.Company, error)
	softDelFn func(id uuid.UUID) error
}

func (f *fakeCompanyRepo) Create(_ context.Context, c model.Company) (*model.Company, error) {
	if f.createFn == nil {
		f.t.Fatal("unexpected call to Create")
	}
	return f.createFn(c)
}

func (f *fakeCompanyRepo) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	if f.getByIDFn == nil {
		f.t.Fatal("unexpected call to GetByID")
	}
	return f.getByIDFn(id)
}

func (f *fakeCompanyRepo) List(_ context.Context, filter model.CompanyFilter, limit, offset int) ([]model.Company, int, error) {
	if f.listFn == nil {
		f.t.Fatal("unexpected call to List")
	}
	return f.listFn(filter, limit, offset)
}

func (f *fakeCompanyRepo) Update(_ context.Context, c model.Company) (*model.Company, error) {
	if f.updateFn == nil {
		f.t.Fatal("unexpected call to Update")
	}
	return f.updateFn(c)
}

func (f *fakeCompanyRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	if f.softDelFn == nil {
		f.t.Fatal("unexpected call to SoftDelete")
	}
	return f.softDelFn(id)
}

func ptr[T any](v T) *T { return &v }

// Principals used across service tests.
var (
	platformAdmin = auth.Principal{UserID: uuid.New()} // no company
	companyA      = uuid.New()
	companyAUser  = auth.Principal{UserID: uuid.New(), CompanyID: &companyA}
)

// requireValidationField asserts err is a *validation.ValidationError containing field.
func requireValidationField(t *testing.T, err error, field string) {
	t.Helper()
	var vErr *validation.ValidationError
	require.True(t, errors.As(err, &vErr), "expected *validation.ValidationError, got %v", err)
	assert.Contains(t, vErr.Fields, field)
}

// ---------- Create ----------

func TestCompanyService_Create_Valid(t *testing.T) {
	var saved model.Company
	repo := &fakeCompanyRepo{t: t, createFn: func(c model.Company) (*model.Company, error) {
		saved = c
		c.ID = uuid.New()
		c.IsActive = true
		return &c, nil
	}}
	svc := NewCompanyService(repo, noAudit())

	got, err := svc.Create(context.Background(), platformAdmin, CreateCompanyInput{
		Name: "  Acme Freight  ", DOTNumber: " 1234567 ", Timezone: "America/Chicago",
	})

	require.NoError(t, err)
	assert.Equal(t, "Acme Freight", saved.Name, "input is trimmed before saving")
	assert.Equal(t, "1234567", saved.DOTNumber)
	assert.Equal(t, model.HOSCycle70in8, saved.HOSCycle, "no cycle given: 70 hours in 8 days")
	assert.NotEqual(t, uuid.Nil, got.ID)
}

func TestCompanyService_Create_Invalid(t *testing.T) {
	valid := CreateCompanyInput{Name: "Acme", DOTNumber: "1234567", Timezone: "America/Chicago"}

	tests := []struct {
		name      string
		modify    func(in *CreateCompanyInput)
		wantField string
	}{
		{"empty name", func(in *CreateCompanyInput) { in.Name = "" }, "name"},
		{"whitespace name", func(in *CreateCompanyInput) { in.Name = "   " }, "name"},
		{"name too long", func(in *CreateCompanyInput) { in.Name = strings.Repeat("a", 201) }, "name"},
		{"dot with letters", func(in *CreateCompanyInput) { in.DOTNumber = "DOT123" }, "dot_number"},
		{"dot too long", func(in *CreateCompanyInput) { in.DOTNumber = "123456789" }, "dot_number"},
		{"empty dot", func(in *CreateCompanyInput) { in.DOTNumber = "" }, "dot_number"},
		{"unknown timezone", func(in *CreateCompanyInput) { in.Timezone = "Mars/Olympus" }, "timezone"},
		{"empty timezone", func(in *CreateCompanyInput) { in.Timezone = "" }, "timezone"},
		{"Local timezone", func(in *CreateCompanyInput) { in.Timezone = "Local" }, "timezone"},
		{"unknown cycle", func(in *CreateCompanyInput) { in.HOSCycle = "us_80_9" }, "hos_cycle"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.modify(&in)
			// No createFn: if validation fails to stop the call, the fake fails the test.
			svc := NewCompanyService(&fakeCompanyRepo{t: t}, noAudit())

			got, err := svc.Create(context.Background(), platformAdmin, in)

			assert.Nil(t, got)
			requireValidationField(t, err, tt.wantField)
		})
	}
}

func TestCompanyService_Create_ReportsAllInvalidFields(t *testing.T) {
	svc := NewCompanyService(&fakeCompanyRepo{t: t}, noAudit())

	_, err := svc.Create(context.Background(), platformAdmin, CreateCompanyInput{})

	var vErr *validation.ValidationError
	require.True(t, errors.As(err, &vErr))
	assert.Len(t, vErr.Fields, 3)
}

func TestCompanyService_Create_RepositoryErrorIsReturned(t *testing.T) {
	repo := &fakeCompanyRepo{t: t, createFn: func(model.Company) (*model.Company, error) {
		return nil, model.ErrDOTNumberTaken
	}}
	svc := NewCompanyService(repo, noAudit())

	_, err := svc.Create(context.Background(), platformAdmin, CreateCompanyInput{
		Name: "Acme", DOTNumber: "1", Timezone: "UTC",
	})

	assert.ErrorIs(t, err, model.ErrDOTNumberTaken)
}

// ---------- GetByID / Delete ----------

func TestCompanyService_GetByID_NotFound(t *testing.T) {
	repo := &fakeCompanyRepo{t: t, getByIDFn: func(uuid.UUID) (*model.Company, error) {
		return nil, model.ErrNotFound
	}}

	_, err := NewCompanyService(repo, noAudit()).GetByID(context.Background(), platformAdmin, uuid.New())

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCompanyService_Delete(t *testing.T) {
	id := uuid.New()
	var deletedID uuid.UUID
	repo := &fakeCompanyRepo{t: t, softDelFn: func(got uuid.UUID) error {
		deletedID = got
		return nil
	}}

	err := NewCompanyService(repo, noAudit()).Delete(context.Background(), platformAdmin, id)

	require.NoError(t, err)
	assert.Equal(t, id, deletedID)
}

// ---------- List ----------

func TestCompanyService_List_Pagination(t *testing.T) {
	tests := []struct {
		name                  string
		in                    ListParams
		wantLimit, wantOffset int
		wantPage, wantSize    int
	}{
		{"defaults", ListParams{}, 20, 0, 1, 20},
		{"third page", ListParams{Page: 3, PageSize: 10}, 10, 20, 3, 10},
		{"negative page becomes 1", ListParams{Page: -5, PageSize: 10}, 10, 0, 1, 10},
		{"page size capped", ListParams{Page: 1, PageSize: 1000}, 100, 0, 1, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotLimit, gotOffset int
			repo := &fakeCompanyRepo{t: t, listFn: func(_ model.CompanyFilter, limit, offset int) ([]model.Company, int, error) {
				gotLimit, gotOffset = limit, offset
				return []model.Company{{Name: "A"}}, 42, nil
			}}

			page, err := NewCompanyService(repo, noAudit()).List(context.Background(), platformAdmin, tt.in)

			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, gotLimit)
			assert.Equal(t, tt.wantOffset, gotOffset)
			assert.Equal(t, tt.wantPage, page.Page)
			assert.Equal(t, tt.wantSize, page.PageSize)
			assert.Equal(t, 42, page.Total)
			assert.Len(t, page.Items, 1)
		})
	}
}

// ---------- Update ----------

func existingCompany() *model.Company {
	return &model.Company{
		ID: uuid.New(), Name: "Acme", DOTNumber: "1111", Timezone: "America/Chicago", HOSCycle: model.HOSCycle70in8,
		IsActive: true, CreatedAt: time.Now(),
	}
}

func TestCompanyService_Update_OnlyChangesProvidedFields(t *testing.T) {
	current := existingCompany()
	var saved model.Company
	repo := &fakeCompanyRepo{
		t: t,
		getByIDFn: func(uuid.UUID) (*model.Company, error) {
			c := *current // return a copy, like a real database would
			return &c, nil
		},
		updateFn: func(c model.Company) (*model.Company, error) {
			saved = c
			return &c, nil
		},
	}

	_, err := NewCompanyService(repo, noAudit()).Update(context.Background(), platformAdmin, current.ID, UpdateCompanyInput{
		Name:     ptr(" Acme LLC "),
		IsActive: ptr(false), // explicit false must be applied, not ignored
	})

	require.NoError(t, err)
	assert.Equal(t, "Acme LLC", saved.Name)
	assert.False(t, saved.IsActive)
	assert.Equal(t, current.DOTNumber, saved.DOTNumber, "not sent, so unchanged")
	assert.Equal(t, current.Timezone, saved.Timezone, "not sent, so unchanged")
	assert.Equal(t, current.HOSCycle, saved.HOSCycle, "not sent, so unchanged")
}

func TestCompanyService_Update_HOSCycle(t *testing.T) {
	current := existingCompany()
	var saved model.Company
	repo := &fakeCompanyRepo{t: t,
		getByIDFn: func(uuid.UUID) (*model.Company, error) { c := *current; return &c, nil },
		updateFn:  func(c model.Company) (*model.Company, error) { saved = c; return &c, nil },
	}
	svc := NewCompanyService(repo, noAudit())

	_, err := svc.Update(context.Background(), platformAdmin, current.ID, UpdateCompanyInput{HOSCycle: ptr("us_60_7")})
	require.NoError(t, err)
	assert.Equal(t, model.HOSCycle60in7, saved.HOSCycle)

	_, err = svc.Update(context.Background(), platformAdmin, current.ID, UpdateCompanyInput{HOSCycle: ptr("weekly")})
	requireValidationField(t, err, "hos_cycle")
}

func TestCompanyService_Update_EmptyInput(t *testing.T) {
	svc := NewCompanyService(&fakeCompanyRepo{t: t}, noAudit()) // must not touch the repo

	_, err := svc.Update(context.Background(), platformAdmin, uuid.New(), UpdateCompanyInput{})

	requireValidationField(t, err, "body")
}

func TestCompanyService_Update_NotFound(t *testing.T) {
	repo := &fakeCompanyRepo{t: t, getByIDFn: func(uuid.UUID) (*model.Company, error) {
		return nil, model.ErrNotFound
	}} // no updateFn: Update must not be called

	_, err := NewCompanyService(repo, noAudit()).Update(context.Background(), platformAdmin, uuid.New(), UpdateCompanyInput{
		Name: ptr("New"),
	})

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCompanyService_Update_InvalidValue(t *testing.T) {
	current := existingCompany()
	repo := &fakeCompanyRepo{t: t, getByIDFn: func(uuid.UUID) (*model.Company, error) {
		c := *current
		return &c, nil
	}} // no updateFn: invalid data must not be saved

	_, err := NewCompanyService(repo, noAudit()).Update(context.Background(), platformAdmin, current.ID, UpdateCompanyInput{
		Timezone: ptr("Not/AZone"),
	})

	requireValidationField(t, err, "timezone")
}

// ---------- Tenant scope ----------

func TestCompanyService_List_ScopedForCompanyUsers(t *testing.T) {
	tests := []struct {
		name       string
		principal  auth.Principal
		wantOnlyID *uuid.UUID
	}{
		{"platform user sees all companies", platformAdmin, nil},
		{"company user sees only their own", companyAUser, &companyA},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got model.CompanyFilter
			repo := &fakeCompanyRepo{t: t, listFn: func(f model.CompanyFilter, _, _ int) ([]model.Company, int, error) {
				got = f
				return nil, 0, nil
			}}

			_, err := NewCompanyService(repo, noAudit()).List(context.Background(), tt.principal, ListParams{})

			require.NoError(t, err)
			assert.Equal(t, tt.wantOnlyID, got.OnlyID)
		})
	}
}

func TestCompanyService_OtherTenantIsNotFound(t *testing.T) {
	otherCompany := uuid.New()
	// No repo functions set: an out-of-scope request must never reach the database.
	svc := NewCompanyService(&fakeCompanyRepo{t: t}, noAudit())

	_, err := svc.GetByID(context.Background(), companyAUser, otherCompany)
	assert.ErrorIs(t, err, model.ErrNotFound, "404, not 403: do not reveal that it exists")

	_, err = svc.Update(context.Background(), companyAUser, otherCompany, UpdateCompanyInput{Name: ptr("Hacked")})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCompanyService_OwnCompany(t *testing.T) {
	own := &model.Company{ID: companyA, Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8, IsActive: true}
	repo := &fakeCompanyRepo{
		t:         t,
		getByIDFn: func(uuid.UUID) (*model.Company, error) { c := *own; return &c, nil },
		updateFn:  func(c model.Company) (*model.Company, error) { return &c, nil },
	}
	svc := NewCompanyService(repo, noAudit())

	t.Run("can read", func(t *testing.T) {
		got, err := svc.GetByID(context.Background(), companyAUser, companyA)
		require.NoError(t, err)
		assert.Equal(t, companyA, got.ID)
	})

	t.Run("can rename", func(t *testing.T) {
		got, err := svc.Update(context.Background(), companyAUser, companyA, UpdateCompanyInput{Name: ptr("Acme LLC")})
		require.NoError(t, err)
		assert.Equal(t, "Acme LLC", got.Name)
	})

	t.Run("cannot suspend or reactivate itself", func(t *testing.T) {
		_, err := svc.Update(context.Background(), companyAUser, companyA, UpdateCompanyInput{IsActive: ptr(true)})
		assert.ErrorIs(t, err, model.ErrForbidden, "field-level rule: is_active is a platform decision")
	})
}

func TestCompanyService_CreateAndDeleteArePlatformOnly(t *testing.T) {
	// Defense in depth: even if a company role were wrongly granted
	// company:create / company:delete, the service refuses.
	svc := NewCompanyService(&fakeCompanyRepo{t: t}, noAudit()) // repo must not be called

	_, err := svc.Create(context.Background(), companyAUser, CreateCompanyInput{Name: "X", DOTNumber: "1", Timezone: "UTC"})
	assert.ErrorIs(t, err, model.ErrForbidden)

	err = svc.Delete(context.Background(), companyAUser, companyA)
	assert.ErrorIs(t, err, model.ErrForbidden, "not even their own company")
}

// noAudit returns an auditor whose entries nobody looks at.
func noAudit() *Auditor {
	a, _ := newTestAuditor()
	return a
}

func TestCompanyService_Audit(t *testing.T) {
	repo := &fakeCompanyRepo{t: t,
		createFn: func(c model.Company) (*model.Company, error) { c.ID = uuid.New(); c.IsActive = true; return &c, nil },
	}
	audit, store := newTestAuditor()
	svc := NewCompanyService(repo, audit)

	c, err := svc.Create(context.Background(), platformAdmin, CreateCompanyInput{Name: "Acme", DOTNumber: "123", Timezone: "UTC"})
	require.NoError(t, err)

	e := store.last(t)
	assert.Equal(t, "company.create", e.Action)
	assert.Equal(t, &c.ID, e.CompanyID, "the company's own history belongs to it")
	assert.Equal(t, "Acme", e.Changes["name"].New)
	store.requireAllInTx(t)
}
