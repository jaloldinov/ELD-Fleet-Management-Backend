package model

import (
	"time"

	"github.com/google/uuid"
)

// Company is a motor carrier: the tenant that owns users, drivers, trucks and ELDs.
type Company struct {
	ID        uuid.UUID
	Name      string
	DOTNumber string // USDOT number
	Timezone  string // home-terminal IANA time zone, e.g. America/Chicago
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time // nil = not deleted (pointer because the column is nullable)
}
