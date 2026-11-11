package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// HOSRecords reads the logbook (repository.DutyStatusRepository).
type HOSRecords interface {
	ListPeriod(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error)
}

// HOSDrivers finds drivers (repository.DriverRepository).
type HOSDrivers interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
}

// HOSService answers "how much may this driver still drive?". It writes
// nothing: the clocks are worked out from the logbook on every request,
// so an edit of the log changes them at once.
type HOSService struct {
	now       func() time.Time // our clock; replaceable in tests
	records   HOSRecords
	drivers   HOSDrivers
	companies DutyCompanies
}

func NewHOSService(records HOSRecords, drivers HOSDrivers, companies DutyCompanies) *HOSService {
	return &HOSService{now: time.Now, records: records, drivers: drivers, companies: companies}
}

// DriverClocks is a driver's clocks, with the zone they are shown in.
type DriverClocks struct {
	DriverID uuid.UUID
	Location *time.Location
	Clocks   model.HOSClocks
}

// MyClocks: the caller's own clocks. ErrNotFound if not a driver.
func (s *HOSService) MyClocks(ctx context.Context, p auth.Principal) (*DriverClocks, error) {
	dd, err := s.drivers.GetByUserID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return s.clocks(ctx, dd.Driver)
}

// DriverClocks: a driver's clocks, for the office. Drivers of other
// companies "do not exist" for company users.
func (s *HOSService) DriverClocks(ctx context.Context, p auth.Principal, driverID uuid.UUID) (*DriverClocks, error) {
	dd, err := s.drivers.GetByID(ctx, driverID)
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && dd.Driver.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return s.clocks(ctx, dd.Driver)
}

func (s *HOSService) clocks(ctx context.Context, d model.Driver) (*DriverClocks, error) {
	company, err := s.companies.GetByID(ctx, d.CompanyID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(company.Timezone)
	if err != nil {
		return nil, fmt.Errorf("company %s time zone %q: %w", company.ID, company.Timezone, err)
	}
	now := s.now()
	from := now.Add(-model.HOSLookback)
	events, err := s.records.ListPeriod(ctx, d.ID, from, now)
	if err != nil {
		return nil, err
	}
	return &DriverClocks{
		DriverID: d.ID, Location: loc,
		Clocks: model.EvaluateHOS(events, from, now, loc, company.HOSCycle),
	}, nil
}
