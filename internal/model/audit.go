package model

import (
	"reflect"
	"time"

	"github.com/google/uuid"
)

// Audited entity types (audit_logs.entity).
const (
	AuditCompany      = "company"
	AuditUser         = "user"
	AuditDriver       = "driver"
	AuditTruck        = "truck"
	AuditELDDevice    = "eld_device"
	AuditRateLimit    = "rate_limit"
	AuditProduct      = "product"
	AuditPrice        = "price"
	AuditSubscription = "subscription"
)

// AuditEntry is one change in the audit log.
type AuditEntry struct {
	ID         uuid.UUID
	OccurredAt time.Time
	ActorID    *uuid.UUID // nil = the system
	ActorName  string     // filled when reading: "Jane Smith"
	CompanyID  *uuid.UUID // tenant of the changed record; nil = platform-level
	Action     string     // "truck.update"
	Entity     string     // AuditTruck
	EntityID   string
	Changes    AuditChanges // nil when nothing to show (e.g. delete)
	RequestID  string
}

// AuditChange is one field's old and new value.
type AuditChange struct {
	Old any
	New any
}

// AuditChanges maps field names (as in the API, e.g. "unit_number") to
// their change.
type AuditChanges map[string]AuditChange

// Set records field going from old to new, only if the value really
// changed. Pointers are followed (nil = no value), so *string and string
// compare by content.
func (c AuditChanges) Set(field string, old, new any) {
	old, new = deref(old), deref(new)
	if reflect.DeepEqual(old, new) {
		return
	}
	c[field] = AuditChange{Old: old, New: new}
}

// deref turns a pointer into its value (nil pointer → nil).
func deref(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer {
		return v
	}
	if rv.IsNil() {
		return nil
	}
	return rv.Elem().Interface()
}

// AuditFilter narrows an audit log list. The zero value means "no filter".
type AuditFilter struct {
	CompanyID *uuid.UUID
	Entity    string
	EntityID  string
	ActorID   *uuid.UUID
	Action    string
	From, To  *time.Time // occurred_at >= From, < To
}
