package repository

import (
	"context"
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

func TestUserRepository_UpdateLastLogin(t *testing.T) {
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewUserRepository(db)
	ctx := context.Background()
	u, err := repo.Create(ctx, newUser(company.ID, "jane@acme.com"), []string{model.RoleAdmin})
	require.NoError(t, err)
	require.Nil(t, u.LastLoginAt, "never logged in yet")

	require.NoError(t, repo.UpdateLastLogin(ctx, u.ID))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.LastLoginAt)
}

func TestUserRepository_GetPermissions_NoRoles(t *testing.T) {
	db := newTestDB(t)

	perms, err := NewUserRepository(db).GetPermissions(context.Background(), uuid.New())

	require.NoError(t, err)
	assert.Empty(t, perms)
}
