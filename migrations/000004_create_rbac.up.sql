-- Role-based access control (RBAC):
--   user --< user_roles >-- role --< role_permissions >-- permission
-- A user's effective permissions = union of the permissions of all their roles.

-- Permissions are defined by the code (e.g. 'truck:create') and seeded by
-- migrations. The code itself is the primary key (a natural key): readable in
-- SQL and one join fewer when loading a user's permissions.
CREATE TABLE permissions (
    code         TEXT PRIMARY KEY,
    description  TEXT NOT NULL,

    CONSTRAINT permissions_code_format CHECK (code ~ '^[a-z_]+:[a-z_]+$')
);

CREATE TABLE roles (
    id           UUID        PRIMARY KEY DEFAULT uuidv7(),
    -- NULL = system role shared by all companies. Non-NULL is reserved for
    -- company-defined custom roles (not implemented yet).
    company_id   UUID        NULL REFERENCES companies (id),
    name         TEXT        NOT NULL,
    -- 'platform' roles are for platform users, 'company' roles for company users.
    scope        TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT roles_scope_valid CHECK (scope IN ('platform', 'company')),
    CONSTRAINT roles_name_format CHECK (name ~ '^[a-z_]{2,50}$')
);

-- System role names are unique; custom role names are unique within a company.
-- Two partial indexes, because NULLs are never equal in a normal UNIQUE.
CREATE UNIQUE INDEX roles_system_name_uniq ON roles (name) WHERE company_id IS NULL;
CREATE UNIQUE INDEX roles_company_name_uniq ON roles (company_id, name) WHERE company_id IS NOT NULL;

CREATE TRIGGER trg_roles_updated_at
    BEFORE UPDATE ON roles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Pure link tables: if either side is really deleted, the link is meaningless,
-- so ON DELETE CASCADE.
CREATE TABLE role_permissions (
    role_id          UUID NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_code  TEXT NOT NULL REFERENCES permissions (code) ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (role_id, permission_code)
);

CREATE TABLE user_roles (
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id     UUID        NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

-- The primary key (user_id, role_id) already serves "roles of user X".
-- This index serves the reverse: "users who have role X".
CREATE INDEX user_roles_role_id_idx ON user_roles (role_id);
