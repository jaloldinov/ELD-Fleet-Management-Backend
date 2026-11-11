package service

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// HOSRecords reads the logbook (repository.DutyStatusRepository).
type HOSRecords interface {
	ListPeriod(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error)
	ListPeriodMany(ctx context.Context, driverIDs []uuid.UUID, from, to time.Time) (map[uuid.UUID][]model.DutyStatusEvent, error)
}

// HOSDrivers finds drivers (repository.DriverRepository).
type HOSDrivers interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error)
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error)
	List(ctx context.Context, f model.DriverFilter, limit, offset int) ([]model.DriverDetails, int, error)
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
	company, loc, err := s.company(ctx, d.CompanyID)
	if err != nil {
		return nil, err
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

func (s *HOSService) company(ctx context.Context, id uuid.UUID) (*model.Company, *time.Location, error) {
	company, err := s.companies.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	loc, err := time.LoadLocation(company.Timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("company %s time zone %q: %w", company.ID, company.Timezone, err)
	}
	return company, loc, nil
}

// ---------- the fleet board ----------

// Fleet board sort orders.
const (
	FleetSortMostTime  = "-can_drive" // who can drive longest first (the default)
	FleetSortLeastTime = "can_drive"  // who runs out first
	FleetSortName      = "name"
)

// FleetHOSParams: whose clocks. Company users always get their own
// company; platform users must name one (the board is in one company's
// time zone and cycle).
type FleetHOSParams struct {
	CompanyID    *uuid.UUID
	DispatcherID *uuid.UUID // only this dispatcher's drivers
	Sort         string
}

// FleetHOSRow is one driver on the board.
type FleetHOSRow struct {
	Driver  model.DriverDetails
	Current *model.DutyStatusEvent // the status in force now; nil = none recorded
	Clocks  model.HOSClocks
}

type FleetHOS struct {
	CompanyID uuid.UUID
	Location  *time.Location
	At        time.Time
	Rows      []FleetHOSRow
}

// fleetPage: drivers are read in pages of this size.
const fleetPage = 500

// FleetBoard answers the dispatcher's question "who can I send, for how
// long?": every driver of the company with their clocks. Two queries for
// the logbook, whatever the fleet's size (ListPeriodMany), not one per
// driver; the clocks are then worked out in memory.
func (s *HOSService) FleetBoard(ctx context.Context, p auth.Principal, params FleetHOSParams) (*FleetHOS, error) {
	companyID := params.CompanyID
	if !p.IsPlatformUser() {
		companyID = p.CompanyID // tenant scope
	}
	switch {
	case companyID == nil:
		return nil, &validation.ValidationError{Fields: map[string]string{"company_id": "is required for platform users"}}
	case params.Sort == "":
		params.Sort = FleetSortMostTime
	case params.Sort != FleetSortMostTime && params.Sort != FleetSortLeastTime && params.Sort != FleetSortName:
		return nil, &validation.ValidationError{Fields: map[string]string{"sort": "must be -can_drive, can_drive or name"}}
	}
	company, loc, err := s.company(ctx, *companyID)
	if err != nil {
		return nil, err
	}

	var drivers []model.DriverDetails
	filter := model.DriverFilter{CompanyID: companyID, DispatcherID: params.DispatcherID}
	for {
		page, total, err := s.drivers.List(ctx, filter, fleetPage, len(drivers))
		if err != nil {
			return nil, err
		}
		drivers = append(drivers, page...)
		if len(page) == 0 || len(drivers) >= total {
			break
		}
	}

	now := s.now()
	from := now.Add(-model.HOSLookback)
	ids := make([]uuid.UUID, len(drivers))
	for i, d := range drivers {
		ids[i] = d.Driver.ID
	}
	records, err := s.records.ListPeriodMany(ctx, ids, from, now)
	if err != nil {
		return nil, err
	}

	board := &FleetHOS{CompanyID: company.ID, Location: loc, At: now, Rows: make([]FleetHOSRow, 0, len(drivers))}
	for _, d := range drivers {
		events := records[d.Driver.ID]
		row := FleetHOSRow{Driver: d, Clocks: model.EvaluateHOS(events, from, now, loc, company.HOSCycle)}
		if n := len(events); n > 0 {
			row.Current = &events[n-1]
		}
		board.Rows = append(board.Rows, row)
	}
	sortFleet(board.Rows, params.Sort)
	return board, nil
}

func sortFleet(rows []FleetHOSRow, order string) {
	byName := func(a, b FleetHOSRow) int {
		return cmp.Or(cmp.Compare(a.Driver.User.LastName, b.Driver.User.LastName),
			cmp.Compare(a.Driver.User.FirstName, b.Driver.User.FirstName),
			cmp.Compare(a.Driver.Driver.ID.String(), b.Driver.Driver.ID.String()))
	}
	slices.SortStableFunc(rows, func(a, b FleetHOSRow) int {
		switch order {
		case FleetSortLeastTime:
			return cmp.Or(cmp.Compare(a.Clocks.CanDriveFor, b.Clocks.CanDriveFor), byName(a, b))
		case FleetSortMostTime:
			return cmp.Or(cmp.Compare(b.Clocks.CanDriveFor, a.Clocks.CanDriveFor), byName(a, b))
		default:
			return byName(a, b)
		}
	})
}
