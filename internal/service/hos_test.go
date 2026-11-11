package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
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
