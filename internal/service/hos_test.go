package service

import (
	"context"
	"fmt"
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

// memHOSCompanies answers with a zone and a cycle.
type memHOSCompanies map[uuid.UUID]model.Company

func (m memHOSCompanies) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	c, ok := m[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &c, nil
}

func TestHOSService(t *testing.T) {
	ctx := context.Background()
	f := newDutyFixture() // a driver, and a logbook in memory
	companies := memHOSCompanies{f.driver.CompanyID: {ID: f.driver.CompanyID, Timezone: "America/Chicago", HOSCycle: model.HOSCycle60in7}}
	svc := NewHOSService(f.events, f.events.drivers, companies)
	svc.now = func() time.Time { return f.clock }

	// Driving since 06:00 UTC (f.clock), for 2 hours.
	inTx := context.WithValue(ctx, inTxKey{}, true)
	_, err := f.events.Insert(inTx, model.DutyStatusEvent{CompanyID: f.driver.CompanyID, DriverID: f.driver.ID,
		Status: model.DutyDriving, StartedAt: f.clock, Origin: model.DutyOriginAuto})
	require.NoError(t, err)
	f.clock = f.clock.Add(2 * time.Hour)

	t.Run("the driver's own clocks, with the company's cycle and zone", func(t *testing.T) {
		c, err := svc.MyClocks(ctx, f.me)

		require.NoError(t, err)
		assert.Equal(t, f.driver.ID, c.DriverID)
		assert.Equal(t, "America/Chicago", c.Location.String())
		assert.Equal(t, model.HOSCycle60in7, c.Clocks.Cycle)
		assert.Equal(t, 2*time.Hour, c.Clocks.DrivingUsed)
		assert.Equal(t, 9*time.Hour, c.Clocks.DrivingLeft)
		assert.Equal(t, 58*time.Hour, c.Clocks.CycleLeft, "60 - 2")
		assert.Equal(t, f.clock, c.Clocks.At)
	})

	t.Run("the office: own company only", func(t *testing.T) {
		office := auth.Principal{UserID: uuid.New(), CompanyID: f.me.CompanyID}
		c, err := svc.DriverClocks(ctx, office, f.driver.ID)
		require.NoError(t, err)
		assert.Equal(t, 6*time.Hour, c.Clocks.CanDriveFor, "the break comes after 8 hours")

		other := uuid.New()
		_, err = svc.DriverClocks(ctx, auth.Principal{UserID: uuid.New(), CompanyID: &other}, f.driver.ID)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("not a driver", func(t *testing.T) {
		_, err := svc.MyClocks(ctx, auth.Principal{UserID: uuid.New(), CompanyID: f.me.CompanyID})
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestHOSService_FleetBoard(t *testing.T) {
	ctx := context.Background()
	f := newDutyFixture()
	company := f.driver.CompanyID
	drivers := f.events.drivers
	drivers.names = map[uuid.UUID]string{f.driver.ID: "Ali"}
	companies := memHOSCompanies{company: {ID: company, Timezone: "America/Chicago", HOSCycle: model.HOSCycle70in8}}
	svc := NewHOSService(f.events, drivers, companies)
	svc.now = func() time.Time { return f.clock }
	inTx := context.WithValue(ctx, inTxKey{}, true)
	dispatcher := uuid.New()

	// Ali drove 9 h; Bob drove 2 h; Cat has never worked; Dan is another
	// company's driver.
	addDriver := func(name string, companyID uuid.UUID, drove time.Duration) model.Driver {
		d := model.Driver{ID: uuid.New(), UserID: uuid.New(), CompanyID: companyID, DispatcherID: &dispatcher}
		drivers.byUser[d.UserID], drivers.names[d.ID] = d, name
		if drove > 0 {
			_, err := f.events.Insert(inTx, model.DutyStatusEvent{CompanyID: companyID, DriverID: d.ID,
				Status: model.DutyDriving, StartedAt: f.clock.Add(-drove), Origin: model.DutyOriginAuto})
			require.NoError(t, err)
		}
		return d
	}
	_, err := f.events.Insert(inTx, model.DutyStatusEvent{CompanyID: company, DriverID: f.driver.ID,
		Status: model.DutyDriving, StartedAt: f.clock.Add(-9 * time.Hour), Origin: model.DutyOriginAuto})
	require.NoError(t, err)
	_, err = f.events.Insert(inTx, model.DutyStatusEvent{CompanyID: company, DriverID: f.driver.ID,
		Status: model.DutyOnDuty, StartedAt: f.clock.Add(-time.Hour), Origin: model.DutyOriginAuto})
	require.NoError(t, err)
	addDriver("Bob", company, 2*time.Hour)
	addDriver("Cat", company, 0)
	addDriver("Dan", uuid.New(), 5*time.Hour)
	office := auth.Principal{UserID: uuid.New(), CompanyID: &company}

	names := func(b *FleetHOS) (out []string) {
		for _, r := range b.Rows {
			out = append(out, r.Driver.User.FirstName)
		}
		return out
	}

	t.Run("who can drive longest first, own company only", func(t *testing.T) {
		b, err := svc.FleetBoard(ctx, office, FleetHOSParams{})
		require.NoError(t, err)
		assert.Equal(t, []string{"Cat", "Bob", "Ali"}, names(b), "8 h (to the break), 6 h (to the break), 3 h (of the 11)")
		assert.Equal(t, "America/Chicago", b.Location.String())

		ali := b.Rows[2]
		require.NotNil(t, ali.Current)
		assert.Equal(t, model.DutyOnDuty, ali.Current.Status, "the status in force now")
		assert.Equal(t, 8*time.Hour, ali.Clocks.DrivingUsed)
		assert.Nil(t, b.Rows[0].Current, "Cat has recorded nothing")
	})

	t.Run("a dispatcher's own drivers", func(t *testing.T) {
		b, err := svc.FleetBoard(ctx, office, FleetHOSParams{DispatcherID: &dispatcher, Sort: FleetSortName})
		require.NoError(t, err)
		assert.Equal(t, []string{"Bob", "Cat"}, names(b), "Ali has no dispatcher")
	})

	t.Run("who runs out first", func(t *testing.T) {
		b, err := svc.FleetBoard(ctx, office, FleetHOSParams{Sort: FleetSortLeastTime})
		require.NoError(t, err)
		assert.Equal(t, []string{"Ali", "Bob", "Cat"}, names(b))
	})

	t.Run("pages through a big fleet", func(t *testing.T) {
		for i := range fleetPage + 3 {
			addDriver(fmt.Sprintf("Extra%03d", i), company, 0)
		}
		t.Cleanup(func() {
			for u, d := range drivers.byUser {
				if strings.HasPrefix(drivers.names[d.ID], "Extra") {
					delete(drivers.byUser, u)
				}
			}
		})
		b, err := svc.FleetBoard(ctx, office, FleetHOSParams{Sort: FleetSortName})
		require.NoError(t, err)
		assert.Len(t, b.Rows, fleetPage+3+3, "two pages read")
	})

	t.Run("platform users name the company; bad sort", func(t *testing.T) {
		_, err := svc.FleetBoard(ctx, auth.Principal{UserID: uuid.New()}, FleetHOSParams{})
		var vErr *validation.ValidationError
		require.ErrorAs(t, err, &vErr)
		assert.Contains(t, vErr.Fields, "company_id")

		b, err := svc.FleetBoard(ctx, auth.Principal{UserID: uuid.New()}, FleetHOSParams{CompanyID: &company, Sort: FleetSortName})
		require.NoError(t, err)
		assert.Len(t, b.Rows, 3)

		_, err = svc.FleetBoard(ctx, office, FleetHOSParams{Sort: "fastest"})
		require.ErrorAs(t, err, &vErr)
		assert.Contains(t, vErr.Fields, "sort")
	})
}
