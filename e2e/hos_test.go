package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hosResp struct {
	DriverID        string `json:"driver_id"`
	At              string `json:"at"`
	CanDriveSeconds int64  `json:"can_drive_seconds"`
	Shift           struct {
		InShift       bool    `json:"in_shift"`
		StartedAt     *string `json:"started_at"`
		WindowEndsAt  *string `json:"window_ends_at"`
		WindowLeftSec int64   `json:"window_left_seconds"`
	} `json:"shift"`
	Driving struct {
		UsedSeconds int64 `json:"used_seconds"`
		LeftSeconds int64 `json:"left_seconds"`
	} `json:"driving"`
	Break struct {
		DrivingSinceSeconds int64 `json:"driving_since_seconds"`
		DueInSeconds        int64 `json:"due_in_seconds"`
	} `json:"break"`
	Cycle struct {
		Type        string `json:"type"`
		UsedSeconds int64  `json:"used_seconds"`
		LeftSeconds int64  `json:"left_seconds"`
	} `json:"cycle"`
	Violations []violationResp `json:"violations"`
	Rest       struct {
		StreakSeconds       int64 `json:"streak_seconds"`
		NewShiftInSeconds   int64 `json:"new_shift_in_seconds"`
		CycleRestartSeconds int64 `json:"cycle_restart_in_seconds"`
	} `json:"rest"`
}

type violationResp struct {
	Rule      string `json:"rule"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Seconds   int64  `json:"seconds"`
}

const hour = int64(3600)

// TestHOSClocks: the example day, as the driver and the office see the
// clocks. Ali started 10 hours ago:
//
//	-10 h on duty (pre-trip)   -9 h driving   -5 h off (lunch, 30 min)
//	-4.5 h driving             -0.5 h on duty (unloading, still)
func TestHOSClocks(t *testing.T) {
	tn := newTenant(t)
	ali, aliC := tn.newDriver(t)
	now := time.Now().Truncate(time.Second)
	for _, e := range []struct {
		status, origin string
		ago            time.Duration
	}{
		{"on_duty", "driver", 10 * time.Hour},
		{"driving", "auto", 9 * time.Hour},
		{"off_duty", "driver", 5 * time.Hour},
		{"driving", "auto", 4*time.Hour + 30*time.Minute},
		{"on_duty", "auto", 30 * time.Minute},
	} {
		_, err := testDB.Exec(t.Context(), `
			INSERT INTO duty_status_events (company_id, driver_id, status, started_at, origin)
			VALUES ($1, $2, $3, $4, $5)`, tn.ID, ali.ID, e.status, now.Add(-e.ago), e.origin)
		require.NoError(t, err)
	}
	const slack = 10 // seconds: time passes between the fixture and the request

	t.Run("the driver's own clocks", func(t *testing.T) {
		var c hosResp
		aliC.do(t, http.MethodGet, "/api/v1/drivers/me/hos", nil, http.StatusOK, &c)

		assert.Equal(t, ali.ID, c.DriverID)
		assert.True(t, c.Shift.InShift)
		assert.Equal(t, 8*hour, c.Driving.UsedSeconds)
		assert.Equal(t, 3*hour, c.Driving.LeftSeconds, "11 - 8")
		assert.InDelta(t, 4*hour, c.Shift.WindowLeftSec, slack, "started 10 h ago: 4 h of the 14 left")
		assert.Zero(t, c.Break.DrivingSinceSeconds, "the 30 minutes unloading were a break")
		assert.Equal(t, "us_70_8", c.Cycle.Type)
		assert.InDelta(t, 9*hour+hour/2, c.Cycle.UsedSeconds, slack, "1 + 4 + 4 + 0.5 (unloading so far)")
		assert.InDelta(t, 3*hour, c.CanDriveSeconds, slack, "the 11-hour limit is the tightest")
		assert.Zero(t, c.Rest.StreakSeconds, "working")
		assert.Equal(t, 10*hour, c.Rest.NewShiftInSeconds)
		assert.Contains(t, c.At, "-0", "in the company's zone (Chicago)")
	})

	t.Run("the office sees the same; a 60/7 company has less cycle", func(t *testing.T) {
		var office hosResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+ali.ID+"/hos", nil, http.StatusOK, &office)
		assert.InDelta(t, 60*hour+hour/2, office.Cycle.LeftSeconds, slack, "70 - 9.5")

		tn.Admin.do(t, http.MethodPatch, "/api/v1/companies/"+tn.ID, map[string]any{"hos_cycle": "us_60_7"}, http.StatusOK, nil)
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+ali.ID+"/hos", nil, http.StatusOK, &office)
		assert.Equal(t, "us_60_7", office.Cycle.Type)
		assert.InDelta(t, 50*hour+hour/2, office.Cycle.LeftSeconds, slack, "60 - 9.5")
	})

	t.Run("a new driver is fully rested", func(t *testing.T) {
		_, bobC := tn.newDriver(t)
		var c hosResp
		bobC.do(t, http.MethodGet, "/api/v1/drivers/me/hos", nil, http.StatusOK, &c)
		assert.False(t, c.Shift.InShift)
		assert.Nil(t, c.Shift.StartedAt)
		assert.Equal(t, 11*hour, c.Driving.LeftSeconds)
		assert.Equal(t, 14*hour, c.Shift.WindowLeftSec)
		assert.Equal(t, 8*hour, c.CanDriveSeconds, "the break comes first")
	})

	t.Run("who may read whose", func(t *testing.T) {
		_, bobC := tn.newDriver(t)
		bobC.fail(t, http.MethodGet, "/api/v1/drivers/"+ali.ID+"/hos", nil, http.StatusForbidden)
		newTenant(t).Admin.fail(t, http.MethodGet, "/api/v1/drivers/"+ali.ID+"/hos", nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/drivers/me/hos", nil, http.StatusNotFound)
	})
}

// TestHOSViolations: yesterday Dan came on duty at 05:00 and drove from
// 06:00 to 19:00 without a break. The clocks and yesterday's log both say
// which rules he broke, and when.
func TestHOSViolations(t *testing.T) {
	tn := newTenant(t)
	dan, danC := tn.newDriver(t)
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	y, m, d := time.Now().In(chicago).AddDate(0, 0, -1).Date()
	at := func(h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, chicago) }
	for _, e := range []struct {
		status, origin string
		at             time.Time
	}{
		{"on_duty", "driver", at(5)},
		{"driving", "auto", at(6)},
		{"off_duty", "driver", at(19)},
	} {
		_, err := testDB.Exec(t.Context(), `
			INSERT INTO duty_status_events (company_id, driver_id, status, started_at, origin)
			VALUES ($1, $2, $3, $4, $5)`, tn.ID, dan.ID, e.status, e.at, e.origin)
		require.NoError(t, err)
	}
	// No window_14h: 05:00 + 14 h = 19:00, and he stopped exactly then.
	want := []violationResp{
		{Rule: "break_30m", StartedAt: at(14).Format(time.RFC3339), EndedAt: at(19).Format(time.RFC3339), Seconds: 5 * hour},
		{Rule: "driving_11h", StartedAt: at(17).Format(time.RFC3339), EndedAt: at(19).Format(time.RFC3339), Seconds: 2 * hour},
	}

	t.Run("the clocks list them", func(t *testing.T) {
		var c hosResp
		danC.do(t, http.MethodGet, "/api/v1/drivers/me/hos", nil, http.StatusOK, &c)
		assert.Equal(t, want, c.Violations)
	})

	t.Run("so does yesterday's log", func(t *testing.T) {
		var l struct {
			Violations []violationResp `json:"violations"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+dan.ID+"/logs/"+at(0).Format(time.DateOnly), nil, http.StatusOK, &l)
		assert.Equal(t, want, l.Violations)
	})

	t.Run("a clean driver has an empty list, not null", func(t *testing.T) {
		_, bobC := tn.newDriver(t)
		r := bobC.send(t, http.MethodGet, "/api/v1/drivers/me/hos", nil)
		r.require(t, http.StatusOK)
		assert.Contains(t, string(r.Body), `"violations":[]`)
	})
}
