package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// HOSService is what HOSHandler needs from the service layer.
type HOSService interface {
	MyClocks(ctx context.Context, p auth.Principal) (*service.DriverClocks, error)
	DriverClocks(ctx context.Context, p auth.Principal, driverID uuid.UUID) (*service.DriverClocks, error)
	FleetBoard(ctx context.Context, p auth.Principal, params service.FleetHOSParams) (*service.FleetHOS, error)
}

// ---------- DTOs ----------

// HOSClocksResponse: where a driver stands on hours of service now. All
// durations are in seconds; "left" is never negative. Times are in the
// company's time zone.
type HOSClocksResponse struct {
	DriverID uuid.UUID `json:"driver_id" swaggertype:"string" format:"uuid"`
	At       time.Time `json:"at"`
	// How long the driver may drive from now: the tightest of the limits.
	CanDriveSeconds int64            `json:"can_drive_seconds" example:"10800"`
	Shift           HOSShiftResponse `json:"shift"`
	Driving         HOSUsageResponse `json:"driving"` // of the 11 hours per shift
	Break           HOSBreakResponse `json:"break"`
	Cycle           HOSCycleResponse `json:"cycle"`
	Rest            HOSRestResponse  `json:"rest"`
	// Limits broken by driving in the last 8 days, oldest first.
	Violations []HOSViolationResponse `json:"violations"`
}

// HOSViolationResponse: driving from started_at to ended_at broke rule.
type HOSViolationResponse struct {
	Rule      string    `json:"rule" enums:"driving_11h,window_14h,break_30m,cycle" example:"driving_11h"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Seconds   int64     `json:"seconds" example:"1800"`
}

func newHOSViolations(vs []model.HOSViolation, loc *time.Location) []HOSViolationResponse {
	out := make([]HOSViolationResponse, 0, len(vs)) // [] not null: "none" is a list
	for _, v := range vs {
		out = append(out, HOSViolationResponse{Rule: string(v.Rule), StartedAt: v.Start.In(loc),
			EndedAt: v.End.In(loc), Seconds: seconds(v.Duration())})
	}
	return out
}

// HOSShiftResponse: a shift begins at the first on-duty moment after 10
// hours of rest. Not in a shift = rested: every clock is full.
type HOSShiftResponse struct {
	InShift       bool       `json:"in_shift"`
	StartedAt     *time.Time `json:"started_at"`
	WindowEndsAt  *time.Time `json:"window_ends_at"` // started_at + 14 h: no driving after it
	WindowLeftSec int64      `json:"window_left_seconds" example:"12600"`
}

type HOSUsageResponse struct {
	UsedSeconds int64 `json:"used_seconds" example:"28800"`
	LeftSeconds int64 `json:"left_seconds" example:"10800"`
}

// HOSBreakResponse: after 8 hours of driving, 30 minutes not driving.
type HOSBreakResponse struct {
	DrivingSinceSeconds int64 `json:"driving_since_seconds" example:"14400"`
	DueInSeconds        int64 `json:"due_in_seconds" example:"14400"` // driving left before the break
}

// HOSCycleResponse: on duty (driving or not) in the rolling days, counted
// from local midnight of the first of them, or from a 34-hour restart.
type HOSCycleResponse struct {
	Type        string    `json:"type" enums:"us_70_8,us_60_7" example:"us_70_8"`
	StartedAt   time.Time `json:"started_at"`
	UsedSeconds int64     `json:"used_seconds" example:"36000"`
	LeftSeconds int64     `json:"left_seconds" example:"216000"`
}

// HOSRestResponse: the current run off duty / in the sleeper (0 while
// working), and what is left of it to a new shift (10 h) and to a cycle
// restart (34 h).
type HOSRestResponse struct {
	StreakSeconds       int64 `json:"streak_seconds" example:"0"`
	NewShiftInSeconds   int64 `json:"new_shift_in_seconds" example:"36000"`
	CycleRestartSeconds int64 `json:"cycle_restart_in_seconds" example:"122400"`
}

func newHOSClocksResponse(dc *service.DriverClocks) HOSClocksResponse {
	c := dc.Clocks
	in := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		v := t.In(dc.Location)
		return &v
	}
	return HOSClocksResponse{
		DriverID: dc.DriverID, At: c.At.In(dc.Location), CanDriveSeconds: seconds(c.CanDriveFor),
		Shift: HOSShiftResponse{InShift: c.InShift, StartedAt: in(c.ShiftStart), WindowEndsAt: in(c.WindowEnd),
			WindowLeftSec: seconds(c.WindowLeft)},
		Driving: HOSUsageResponse{UsedSeconds: seconds(c.DrivingUsed), LeftSeconds: seconds(c.DrivingLeft)},
		Break:   HOSBreakResponse{DrivingSinceSeconds: seconds(c.DrivingSinceBreak), DueInSeconds: seconds(c.BreakDueIn)},
		Cycle: HOSCycleResponse{Type: string(c.Cycle), StartedAt: c.CycleStart.In(dc.Location),
			UsedSeconds: seconds(c.CycleUsed), LeftSeconds: seconds(c.CycleLeft)},
		Rest: HOSRestResponse{StreakSeconds: seconds(c.RestStreak),
			NewShiftInSeconds:   seconds(max(model.ShiftRest-c.RestStreak, 0)),
			CycleRestartSeconds: seconds(max(model.CycleRestart-c.RestStreak, 0))},
		Violations: newHOSViolations(c.Violations, dc.Location),
	}
}

// FleetHOSResponse: every driver of the company with their clocks.
type FleetHOSResponse struct {
	CompanyID uuid.UUID          `json:"company_id" swaggertype:"string" format:"uuid"`
	Timezone  string             `json:"timezone" example:"America/Chicago"`
	At        time.Time          `json:"at"`
	Items     []FleetHOSItemResp `json:"items"`
}

// FleetHOSItemResp is one driver's line on the dispatcher's board.
type FleetHOSItemResp struct {
	DriverID  uuid.UUID `json:"driver_id" swaggertype:"string" format:"uuid"`
	FirstName string    `json:"first_name" example:"Ali"`
	LastName  string    `json:"last_name" example:"Valiyev"`
	// The status in force now and since when; null = none recorded.
	CurrentStatus *string    `json:"current_status" enums:"off_duty,sleeper_berth,driving,on_duty" example:"driving"`
	CurrentSince  *time.Time `json:"current_since"`
	// How long the driver may drive from now, and the limit that decides it.
	CanDriveSeconds int64  `json:"can_drive_seconds" example:"10800"`
	LimitedBy       string `json:"limited_by" enums:"driving_11h,window_14h,break_30m,cycle" example:"driving_11h"`
	InShift         bool   `json:"in_shift"`
	DrivingLeft     int64  `json:"driving_left_seconds" example:"10800"`
	WindowLeft      int64  `json:"window_left_seconds" example:"12600"`
	BreakDueIn      int64  `json:"break_due_in_seconds" example:"14400"`
	CycleLeft       int64  `json:"cycle_left_seconds" example:"216000"`
	RestStreak      int64  `json:"rest_streak_seconds" example:"0"`
	// Violations in the last 8 days (details: GET /drivers/{id}/hos).
	Violations int `json:"violations" example:"0"`
}

func newFleetHOSResponse(b *service.FleetHOS) FleetHOSResponse {
	r := FleetHOSResponse{CompanyID: b.CompanyID, Timezone: b.Location.String(), At: b.At.In(b.Location),
		Items: make([]FleetHOSItemResp, 0, len(b.Rows))}
	for _, row := range b.Rows {
		c := row.Clocks
		item := FleetHOSItemResp{
			DriverID: row.Driver.Driver.ID, FirstName: row.Driver.User.FirstName, LastName: row.Driver.User.LastName,
			CanDriveSeconds: seconds(c.CanDriveFor), LimitedBy: string(c.LimitedBy()), InShift: c.InShift,
			DrivingLeft: seconds(c.DrivingLeft), WindowLeft: seconds(c.WindowLeft), BreakDueIn: seconds(c.BreakDueIn),
			CycleLeft: seconds(c.CycleLeft), RestStreak: seconds(c.RestStreak), Violations: len(c.Violations),
		}
		if row.Current != nil {
			status, since := string(row.Current.Status), row.Current.StartedAt.In(b.Location)
			item.CurrentStatus, item.CurrentSince = &status, &since
		}
		r.Items = append(r.Items, item)
	}
	return r
}

// ---------- Handler ----------

type HOSHandler struct {
	svc HOSService
}

func NewHOSHandler(svc HOSService) *HOSHandler {
	return &HOSHandler{svc: svc}
}

// MyClocks handles GET /api/v1/drivers/me/hos.
//
//	@Summary		My hours of service now
//	@Description	For the driver app: how long the caller may still drive, and each clock behind it (11-hour driving, 14-hour window, 30-minute break, 60/70-hour cycle). Worked out from the logbook on every request. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			hos
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	HOSClocksResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me/hos [get]
func (h *HOSHandler) MyClocks(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	dc, err := h.svc.MyClocks(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newHOSClocksResponse(dc))
}

// DriverClocks handles GET /api/v1/drivers/:id/hos.
//
//	@Summary		A driver's hours of service now
//	@Description	Requires permission: logbook:read
//	@Description	How long the driver may still drive, and each clock behind it (11-hour driving, 14-hour window, 30-minute break, 60/70-hour cycle).
//	@Tags			hos
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Driver ID"	format(uuid)
//	@Success		200	{object}	HOSClocksResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission logbook:read"
//	@Failure		404	{object}	apierror.Response	"driver not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/{id}/hos [get]
func (h *HOSHandler) DriverClocks(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	dc, err := h.svc.DriverClocks(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newHOSClocksResponse(dc))
}

// FleetBoard handles GET /api/v1/hos.
//
//	@Summary		The fleet's hours of service
//	@Description	Requires permission: logbook:read
//	@Description	The dispatcher's board: every driver of the company with their status now, how long they may still drive and the limit that decides it. Sorted by who can drive longest (-can_drive, the default), who runs out first (can_drive) or name.
//	@Tags			hos
//	@Produce		json
//	@Security		BearerAuth
//	@Param			dispatcher_id	query		string	false	"Drivers of this dispatcher (a user id, or \"me\")"
//	@Param			sort			query		string	false	"Order"									Enums(-can_drive, can_drive, name)
//	@Param			company_id		query		string	false	"Platform users only (then required)"	format(uuid)
//	@Success		200				{object}	FleetHOSResponse
//	@Failure		400				{object}	apierror.Response	"invalid filter or sort"
//	@Failure		401				{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402				{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403				{object}	apierror.Response	"missing permission logbook:read"
//	@Failure		404				{object}	apierror.Response	"company not found"
//	@Failure		500				{object}	apierror.Response
//	@Router			/api/v1/hos [get]
func (h *HOSHandler) FleetBoard(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var v validation.Validator
	params := service.FleetHOSParams{Sort: c.Query("sort")}
	if s := c.Query("company_id"); s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "company_id", "must be a valid UUID")
		params.CompanyID = &id
	}
	// "me": the caller's own drivers, without having to know their user id.
	if s := c.Query("dispatcher_id"); s == "me" {
		params.DispatcherID = &p.UserID
	} else if s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "dispatcher_id", `must be a valid UUID or "me"`)
		params.DispatcherID = &id
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}
	b, err := h.svc.FleetBoard(c.Request.Context(), p, params)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newFleetHOSResponse(b))
}
