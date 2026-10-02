package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func strPtr(s string) *string { return &s }

// newUser returns a valid company user; tests change what they need.
func newUser(companyID uuid.UUID, email string) model.User {
	return model.User{
		CompanyID:    &companyID,
		Email:        strPtr(email),
		PasswordHash: "$2a$04$fakehashfakehashfakehashfakehashfakehashfakehashfakeh",
		FirstName:    "Jane",
		LastName:     "Smith",
	}
}

func countUsers(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n))
	return n
}

func TestUserRepository_Create_WithRoles(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)

	u, err := repo.Create(context.Background(), newUser(company.ID, "jane@acme.com"),
		[]string{model.RoleDispatcher, model.RoleManager})

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, u.ID)
	require.NotNil(t, u.CompanyID)
	assert.Equal(t, company.ID, *u.CompanyID)
	assert.Equal(t, "jane@acme.com", *u.Email)
	assert.Nil(t, u.Username)
	assert.True(t, u.IsActive)

	perms, err := repo.GetPermissions(context.Background(), u.ID)
	require.NoError(t, err)
	// manager ∪ dispatcher = manager's 14 permissions (dispatcher's 4 are a subset).
	assert.Len(t, perms, 14)
	assert.Contains(t, perms, model.PermTruckCreate)
	assert.NotContains(t, perms, model.PermUserCreate, "only admin may create users")
}

func TestUserRepository_Create_UnknownRoleRollsBackUser(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)

	_, err := repo.Create(context.Background(), newUser(company.ID, "jane@acme.com"),
		[]string{model.RoleDispatcher, "no_such_role"})

	assert.ErrorIs(t, err, model.ErrRoleNotFound)
	assert.Equal(t, 0, countUsers(t, db), "transaction must roll back the user insert")
}

func TestUserRepository_Create_Duplicates(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()
	roles := []string{model.RoleDriver}

	first := newUser(company.ID, "jane@acme.com")
	first.Username = strPtr("jane")
	_, err := repo.Create(ctx, first, roles)
	require.NoError(t, err)

	t.Run("email differs only by case", func(t *testing.T) {
		_, err := repo.Create(ctx, newUser(company.ID, "JANE@ACME.COM"), roles)
		assert.ErrorIs(t, err, model.ErrEmailTaken)
	})

	t.Run("username differs only by case", func(t *testing.T) {
		u := newUser(company.ID, "other@acme.com")
		u.Username = strPtr("JANE")
		_, err := repo.Create(ctx, u, roles)
		assert.ErrorIs(t, err, model.ErrUsernameTaken)
	})
}

func TestUserRepository_GetByLogin(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()

	staff, err := repo.Create(ctx, newUser(company.ID, "jane@acme.com"), []string{model.RoleAdmin})
	require.NoError(t, err)

	driverIn := newUser(company.ID, "")
	driverIn.Email = nil
	driverIn.Username = strPtr("driver.bob")
	driver, err := repo.Create(ctx, driverIn, []string{model.RoleDriver})
	require.NoError(t, err)

	tests := []struct {
		name   string
		login  string
		wantID uuid.UUID
	}{
		{"by email", "jane@acme.com", staff.ID},
		{"by email, other case", "Jane@ACME.com", staff.ID},
		{"by username", "driver.bob", driver.ID},
		{"by username, other case", "DRIVER.BOB", driver.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := repo.GetByLogin(ctx, tt.login)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, u.ID)
		})
	}

	t.Run("unknown login", func(t *testing.T) {
		_, err := repo.GetByLogin(ctx, "nobody@acme.com")
		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("soft-deleted user cannot be found", func(t *testing.T) {
		_, err := db.Exec(ctx, `UPDATE users SET deleted_at = now() WHERE id = $1`, staff.ID)
		require.NoError(t, err)

		_, err = repo.GetByLogin(ctx, "jane@acme.com")
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestUserRepository_PlatformUser(t *testing.T) {
	db := newTestDB(t)
	repo := NewUserRepository(db)
	ctx := context.Background()

	admin := model.User{
		Email: strPtr("root@eld.local"), PasswordHash: "x", FirstName: "Root", LastName: "Admin",
	}
	u, err := repo.Create(ctx, admin, []string{model.RolePlatformAdmin})
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.True(t, got.IsPlatformUser())

	perms, err := repo.GetPermissions(ctx, u.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, model.AllPermissions(), perms)
}

func TestUserRepository_GetPermissions_InactiveOrDeletedUserHasNone(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()

	tests := []struct {
		name   string
		update string
	}{
		{"deactivated", `UPDATE users SET is_active = false WHERE id = $1`},
		{"soft-deleted", `UPDATE users SET deleted_at = now() WHERE id = $1`},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := repo.Create(ctx, newUser(company.ID, fmt.Sprintf("user%d@acme.com", i)), []string{model.RoleAdmin})
			require.NoError(t, err)
			before, err := repo.GetPermissions(ctx, u.ID)
			require.NoError(t, err)
			require.NotEmpty(t, before)

			_, err = db.Exec(ctx, tt.update, u.ID)
			require.NoError(t, err)

			after, err := repo.GetPermissions(ctx, u.ID)
			require.NoError(t, err)
			assert.Empty(t, after, "access must end immediately, even with a valid token")
		})
	}
}

func TestUserRepository_GetPermissions_NoRoles(t *testing.T) {
	db := newTestDB(t)

	perms, err := NewUserRepository(db).GetPermissions(context.Background(), uuid.New())

	require.NoError(t, err)
	assert.Empty(t, perms)
}

func TestUserRepository_List_ScopedByCompany(t *testing.T) {
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	alpha := createCompany(t, companies, "Alpha", "1")
	bravo := createCompany(t, companies, "Bravo", "2")
	repo := NewUserRepository(db)
	ctx := context.Background()

	for i, c := range []uuid.UUID{alpha.ID, alpha.ID, bravo.ID} {
		_, err := repo.Create(ctx, newUser(c, fmt.Sprintf("u%d@x.com", i)), []string{model.RoleDriver})
		require.NoError(t, err)
	}
	_, err := repo.Create(ctx, model.User{Email: strPtr("root@x.com"), PasswordHash: "x", FirstName: "R", LastName: "A"},
		[]string{model.RolePlatformAdmin})
	require.NoError(t, err)

	t.Run("one company", func(t *testing.T) {
		users, total, err := repo.List(ctx, model.UserFilter{CompanyID: &alpha.ID}, 20, 0)
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		for _, u := range users {
			assert.Equal(t, alpha.ID, *u.CompanyID, "no other tenant's users")
		}
	})

	t.Run("no filter (platform view) includes platform users", func(t *testing.T) {
		_, total, err := repo.List(ctx, model.UserFilter{}, 20, 0)
		require.NoError(t, err)
		assert.Equal(t, 4, total)
	})
}

func TestUserRepository_GetRoles_OneQueryForManyUsers(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()
	a, err := repo.Create(ctx, newUser(company.ID, "a@x.com"), []string{model.RoleManager, model.RoleDispatcher})
	require.NoError(t, err)
	b, err := repo.Create(ctx, newUser(company.ID, "b@x.com"), []string{model.RoleDriver})
	require.NoError(t, err)

	roles, err := repo.GetRoles(ctx, []uuid.UUID{a.ID, b.ID, uuid.New()})

	require.NoError(t, err)
	assert.Equal(t, []string{model.RoleDispatcher, model.RoleManager}, roles[a.ID], "sorted by name")
	assert.Equal(t, []string{model.RoleDriver}, roles[b.ID])
	assert.Len(t, roles, 2, "unknown ids are simply absent")
}

func TestUserRepository_Update(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()
	u, err := repo.Create(ctx, newUser(company.ID, "jane@x.com"), []string{model.RoleDispatcher})
	require.NoError(t, err)

	t.Run("fields only, roles unchanged (nil)", func(t *testing.T) {
		changed := *u
		changed.FirstName, changed.IsActive = "Janet", false

		got, err := repo.Update(ctx, changed, nil)

		require.NoError(t, err)
		assert.Equal(t, "Janet", got.FirstName)
		assert.False(t, got.IsActive)
		roles, _ := repo.GetRoles(ctx, []uuid.UUID{u.ID})
		assert.Equal(t, []string{model.RoleDispatcher}, roles[u.ID])
	})

	t.Run("roles replaced", func(t *testing.T) {
		_, err := repo.Update(ctx, *u, []string{model.RoleManager, model.RoleAdmin})

		require.NoError(t, err)
		roles, _ := repo.GetRoles(ctx, []uuid.UUID{u.ID})
		assert.Equal(t, []string{model.RoleAdmin, model.RoleManager}, roles[u.ID])
	})

	t.Run("unknown role rolls back everything", func(t *testing.T) {
		changed := *u
		changed.FirstName = "ShouldNotBeSaved"

		_, err := repo.Update(ctx, changed, []string{model.RoleDriver, "no_such_role"})

		assert.ErrorIs(t, err, model.ErrRoleNotFound)
		got, _ := repo.GetByID(ctx, u.ID)
		assert.NotEqual(t, "ShouldNotBeSaved", got.FirstName, "name change rolled back")
		roles, _ := repo.GetRoles(ctx, []uuid.UUID{u.ID})
		assert.Equal(t, []string{model.RoleAdmin, model.RoleManager}, roles[u.ID], "old roles kept: DELETE rolled back")
	})

	t.Run("unknown user", func(t *testing.T) {
		_, err := repo.Update(ctx, model.User{ID: uuid.New(), FirstName: "X", LastName: "Y"}, nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestUserRepository_SoftDelete(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()
	u, err := repo.Create(ctx, newUser(company.ID, "jane@x.com"), []string{model.RoleDriver})
	require.NoError(t, err)

	require.NoError(t, repo.SoftDelete(ctx, u.ID))

	_, err = repo.GetByID(ctx, u.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, repo.SoftDelete(ctx, u.ID), model.ErrNotFound, "second delete")

	_, err = repo.Create(ctx, newUser(company.ID, "jane@x.com"), []string{model.RoleDriver})
	assert.NoError(t, err, "email can be reused after soft delete")
}
