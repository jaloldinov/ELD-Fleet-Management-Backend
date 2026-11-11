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
)

// DutyStatusService is what DutyStatusHandler needs from the service layer.
type DutyStatusService interface {
	MyStatus(ctx context.Context, p auth.Principal) (*model.DutyStatusEvent, error)
	ChangeMyStatus(ctx context.Context, p auth.Principal, in service.ChangeDutyStatusInput) (*model.DutyStatusEvent, bool, error)
	MyDailyLog(ctx context.Context, p auth.Principal, date string) (*model.DailyLog, error)
	DriverDailyLog(ctx context.Context, p auth.Principal, driverID uuid.UUID, date string) (*model.DailyLog, error)
	AddMyRecord(ctx context.Context, p auth.Principal, in service.DutyRecordInput) (*model.DutyStatusEvent, error)
	ChangeMyRecord(ctx context.Context, p auth.Principal, id uuid.UUID, in service.DutyRecordInput) (*model.DutyStatusEvent, error)
	CertifyMyLog(ctx context.Context, p auth.Principal, date string) (*model.DailyLog, bool, error)
}

// ---------- DTOs ----------

type ChangeDutyStatusRequest struct {
	// driving is not accepted: the ELD records it from the truck's movement.
	Status string `json:"status" enums:"off_duty,sleeper_berth,on_duty" example:"on_duty"`
	// personal_conveyance only with off_duty, yard_moves only with on_duty;
	// both need a truck.
	Special *string `json:"special,omitempty" enums:"personal_conveyance,yard_moves"`
	Note    *string `json:"note,omitempty" example:"pre-trip inspection" maxLength:"500"`
}

// DutyStatusEventResponse is one logbook record. The status lasts from
// started_at until the driver's next record starts. truck_id and the
// readings are null when unknown (no truck, no ELD reading).
type DutyStatusEventResponse struct {
	ID          uuid.UUID  `json:"id" swaggertype:"string" format:"uuid"`
	DriverID    uuid.UUID  `json:"driver_id" swaggertype:"string" format:"uuid"`
	Status      string     `json:"status" enums:"off_duty,sleeper_berth,driving,on_duty" example:"on_duty"`
	Special     *string    `json:"special" enums:"personal_conveyance,yard_moves"`
	StartedAt   time.Time  `json:"started_at" example:"2026-11-11T06:00:00Z"`
	Origin      string     `json:"origin" enums:"auto,driver" example:"driver"`
	TruckID     *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"`
	Latitude    *float64   `json:"latitude" example:"32.7767"`
	Longitude   *float64   `json:"longitude" example:"-96.797"`
	OdometerMi  *float64   `json:"odometer_mi" example:"120500.5"`
	EngineHours *float64   `json:"engine_hours" example:"8012.3"`
	Note        *string    `json:"note"`
	CreatedAt   time.Time  `json:"created_at"`
	// active = part of the log; inactive = replaced by an edit (history).
	RecordStatus string `json:"record_status" enums:"active,inactive" example:"active"`
	// On an edit: the record it replaced. On an edit or an added record: why.
	Replaces   *uuid.UUID `json:"replaces" swaggertype:"string" format:"uuid"`
	EditReason *string    `json:"edit_reason" example:"forgot to press off duty"`
	// On a replaced record: when the edit took it out.
	InactivatedAt *time.Time `json:"inactivated_at"`
}

// DutyRecordRequest is a record as the driver says it should be.
type DutyRecordRequest struct {
	// driving is not accepted: the ELD records it.
	Status    string     `json:"status" enums:"off_duty,sleeper_berth,on_duty" example:"off_duty"`
	Special   *string    `json:"special,omitempty" enums:"personal_conveyance,yard_moves"`
	StartedAt *time.Time `json:"started_at" example:"2026-11-10T16:00:00-06:00"`
	Note      *string    `json:"note,omitempty" maxLength:"500"`
	// Why the log is being fixed: 4 to 60 characters, required.
	Reason string `json:"reason" example:"forgot to press off duty" minLength:"4" maxLength:"60"`
}

// MyDutyStatusResponse: current is null when the driver has never
// recorded a status.
type MyDutyStatusResponse struct {
	Current *DutyStatusEventResponse `json:"current"`
}

// DutySegmentResponse is a piece of the day in one status: the record in
// force (which may have started the day before), cut to the day.
type DutySegmentResponse struct {
	StartedAt       time.Time               `json:"started_at" example:"2026-11-10T06:00:00-06:00"`
	EndedAt         time.Time               `json:"ended_at" example:"2026-11-10T07:00:00-06:00"`
	DurationSeconds int64                   `json:"duration_seconds" example:"3600"`
	Record          DutyStatusEventResponse `json:"record"`
}

// DutyTotalsResponse: seconds on each line of the grid. Personal
// conveyance counts as off duty, yard moves as on duty.
type DutyTotalsResponse struct {
	OffDuty      int64 `json:"off_duty" example:"50400"`
	SleeperBerth int64 `json:"sleeper_berth" example:"0"`
	Driving      int64 `json:"driving" example:"28800"`
	OnDuty       int64 `json:"on_duty" example:"7200"`
}

// DailyLogResponse is one driver's day, midnight to midnight in the
// company's time zone (23 or 25 hours on the daylight-saving days).
// totals + unrecorded_seconds = recorded_until - starts_at.
type DailyLogResponse struct {
	DriverID uuid.UUID `json:"driver_id" swaggertype:"string" format:"uuid"`
	Date     string    `json:"date" example:"2026-11-10"`
	Timezone string    `json:"timezone" example:"America/Chicago"`
	StartsAt time.Time `json:"starts_at" example:"2026-11-10T00:00:00-06:00"`
	EndsAt   time.Time `json:"ends_at" example:"2026-11-11T00:00:00-06:00"`
	// The log is known up to here: ends_at for a past day, now for today.
	RecordedUntil time.Time             `json:"recorded_until" example:"2026-11-11T00:00:00-06:00"`
	Segments      []DutySegmentResponse `json:"segments"`
	Totals        DutyTotalsResponse    `json:"totals"`
	// Before the driver's first record ever (a new driver).
	UnrecordedSeconds int64 `json:"unrecorded_seconds" example:"0"`
	// Records of the day that edits replaced, oldest first.
	History       []DutyStatusEventResponse `json:"history"`
	Certification CertificationResponse     `json:"certification"`
}

// CertificationResponse: certified = signed and not changed since.
type CertificationResponse struct {
	Certified    bool       `json:"certified"`
	CertifiedAt  *time.Time `json:"certified_at"`   // the latest signature; null = never
	LastChangeAt *time.Time `json:"last_change_at"` // null = no records
}

func newDailyLogResponse(l *model.DailyLog) DailyLogResponse {
	// Times in the company's zone: "06:00" on the grid is 06:00 there.
	in := func(t time.Time) time.Time { return t.In(l.Location) }
	r := DailyLogResponse{
		DriverID: l.DriverID, Date: l.Date, Timezone: l.Location.String(),
		StartsAt: in(l.Start), EndsAt: in(l.End), RecordedUntil: in(l.Until),
		Segments: make([]DutySegmentResponse, 0, len(l.Segments)), // [] not null
		Totals: DutyTotalsResponse{
			OffDuty: seconds(l.Totals[model.DutyOffDuty]), SleeperBerth: seconds(l.Totals[model.DutySleeperBerth]),
			Driving: seconds(l.Totals[model.DutyDriving]), OnDuty: seconds(l.Totals[model.DutyOnDuty]),
		},
		UnrecordedSeconds: seconds(l.Unrecorded),
	}
	r.History = make([]DutyStatusEventResponse, 0, len(l.History))
	for i := range l.History {
		h := newDutyStatusEventResponse(&l.History[i])
		h.StartedAt = in(h.StartedAt)
		r.History = append(r.History, h)
	}
	r.Certification = CertificationResponse{Certified: l.Certified(), CertifiedAt: l.CertifiedAt}
	if !l.LastChangeAt.IsZero() {
		at := l.LastChangeAt
		r.Certification.LastChangeAt = &at
	}
	for _, sg := range l.Segments {
		rec := newDutyStatusEventResponse(&sg.Event)
		rec.StartedAt = in(rec.StartedAt)
		r.Segments = append(r.Segments, DutySegmentResponse{
			StartedAt: in(sg.Start), EndedAt: in(sg.End), DurationSeconds: seconds(sg.Duration()), Record: rec,
		})
	}
	return r
}

// seconds rounds down: segments of a whole-second log are whole seconds.
func seconds(d time.Duration) int64 { return int64(d / time.Second) }

func newDutyStatusEventResponse(e *model.DutyStatusEvent) DutyStatusEventResponse {
	r := DutyStatusEventResponse{
		ID: e.ID, DriverID: e.DriverID, Status: string(e.Status), StartedAt: e.StartedAt,
		Origin: string(e.Origin), TruckID: e.TruckID, Latitude: e.Latitude, Longitude: e.Longitude,
		OdometerMi: e.OdometerMi, EngineHours: e.EngineHours, Note: e.Note, CreatedAt: e.CreatedAt,
		RecordStatus: string(model.RecordActive), Replaces: e.Replaces, EditReason: e.EditReason,
		InactivatedAt: e.InactivatedAt,
	}
	if e.RecordStatus != "" {
		r.RecordStatus = string(e.RecordStatus)
	}
	if e.Special != nil {
		s := string(*e.Special)
		r.Special = &s
	}
	return r
}

// ---------- Handler ----------

type DutyStatusHandler struct {
	svc DutyStatusService
}

func NewDutyStatusHandler(svc DutyStatusService) *DutyStatusHandler {
	return &DutyStatusHandler{svc: svc}
}

// MyStatus handles GET /api/v1/drivers/me/duty-status.
//
//	@Summary		My duty status now
//	@Description	For the driver app: the caller's status in force now (null if none recorded yet). Needs a login, no extra permission. Not behind the billing gate: hours of service must be logged by law.
//	@Tags			logbook
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MyDutyStatusResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status [get]
func (h *DutyStatusHandler) MyStatus(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	e, err := h.svc.MyStatus(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	var res MyDutyStatusResponse
	if e != nil {
		r := newDutyStatusEventResponse(e)
		res.Current = &r
	}
	c.JSON(http.StatusOK, res)
}

// ChangeMyStatus handles POST /api/v1/drivers/me/duty-status.
//
//	@Summary		Change my duty status
//	@Description	The driver presses a status button; the change starts NOW (server clock). Truck, position, odometer and engine hours are taken from the driver's truck and its ELD. Pressing the status already in force changes nothing (200 with that record); a real change is 201. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		ChangeDutyStatusRequest	true	"New status"
//	@Success		201		{object}	DutyStatusEventResponse	"recorded"
//	@Success		200		{object}	DutyStatusEventResponse	"already in that status"
//	@Failure		400		{object}	apierror.Response		"invalid status, special or note"
//	@Failure		401		{object}	apierror.Response		"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response		"the caller is not a driver"
//	@Failure		409		{object}	apierror.Response		"another change in the same second, or the truck is moving with the caller at the wheel"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status [post]
func (h *DutyStatusHandler) ChangeMyStatus(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req ChangeDutyStatusRequest
	if !bindJSON(c, &req) {
		return
	}
	in := service.ChangeDutyStatusInput{Status: model.DutyStatus(req.Status), Note: req.Note}
	if req.Special != nil {
		s := model.SpecialDriving(*req.Special)
		in.Special = &s
	}
	e, created, err := h.svc.ChangeMyStatus(c.Request.Context(), p, in)
	if err != nil {
		handleError(c, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	c.JSON(code, newDutyStatusEventResponse(e))
}

// MyDailyLog handles GET /api/v1/drivers/me/logs/:date.
//
//	@Summary		My log of a day
//	@Description	For the driver app (and the roadside inspector looking at it): the caller's day, midnight to midnight in the company's time zone, as segments with durations and totals per status. Today is shown up to now. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Produce		json
//	@Security		BearerAuth
//	@Param			date	path		string	true	"Day, YYYY-MM-DD"	example(2026-11-10)
//	@Success		200		{object}	DailyLogResponse
//	@Failure		400		{object}	apierror.Response	"invalid or future date"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/logs/{date} [get]
func (h *DutyStatusHandler) MyDailyLog(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	l, err := h.svc.MyDailyLog(c.Request.Context(), p, c.Param("date"))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDailyLogResponse(l))
}

// DriverDailyLog handles GET /api/v1/drivers/:id/logs/:date.
//
//	@Summary		A driver's log of a day
//	@Description	Requires permission: logbook:read
//	@Description	The office's view of a driver's day: midnight to midnight in the company's time zone, as segments with durations and totals per status. Today is shown up to now.
//	@Tags			logbook
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Driver ID"			format(uuid)
//	@Param			date	path		string	true	"Day, YYYY-MM-DD"	example(2026-11-10)
//	@Success		200		{object}	DailyLogResponse
//	@Failure		400		{object}	apierror.Response	"invalid id, or invalid or future date"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402		{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403		{object}	apierror.Response	"missing permission logbook:read"
//	@Failure		404		{object}	apierror.Response	"driver not found"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/{id}/logs/{date} [get]
func (h *DutyStatusHandler) DriverDailyLog(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	l, err := h.svc.DriverDailyLog(c.Request.Context(), p, id, c.Param("date"))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDailyLogResponse(l))
}

func bindDutyRecord(c *gin.Context) (service.DutyRecordInput, bool) {
	var req DutyRecordRequest
	if !bindJSON(c, &req) {
		return service.DutyRecordInput{}, false
	}
	in := service.DutyRecordInput{Status: model.DutyStatus(req.Status), Note: req.Note, Reason: req.Reason}
	if req.StartedAt != nil {
		in.StartedAt = *req.StartedAt
	}
	if req.Special != nil {
		sp := model.SpecialDriving(*req.Special)
		in.Special = &sp
	}
	return in, true
}

// AddMyRecord handles POST /api/v1/drivers/me/duty-status/records.
//
//	@Summary		Add a record I forgot
//	@Description	The driver enters a past status change they did not record ("I went off duty at 16:00 but did not press the button"). Within the last 8 days, with a reason. Refused if it would change when the driver was driving: driving belongs to the ELD. The day needs certifying again. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		DutyRecordRequest	true	"The record"
//	@Success		201		{object}	DutyStatusEventResponse
//	@Failure		400		{object}	apierror.Response	"invalid status, time or reason"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response	"the caller is not a driver"
//	@Failure		409		{object}	apierror.Response	"would change driving, or a record starts at that moment"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status/records [post]
func (h *DutyStatusHandler) AddMyRecord(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	in, ok := bindDutyRecord(c)
	if !ok {
		return
	}
	e, err := h.svc.AddMyRecord(c.Request.Context(), p, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newDutyStatusEventResponse(e))
}

// ChangeMyRecord handles PUT /api/v1/drivers/me/duty-status/records/:id.
//
//	@Summary		Correct one of my records
//	@Description	Replaces the record with the version in the body (the whole record). The old version is kept as inactive history; the new one points to it. Not for driving records, and refused if it would change when the driver was driving. Within the last 8 days, with a reason. The day needs certifying again. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string					true	"Record ID (the newest version)"	format(uuid)
//	@Param			body	body		DutyRecordRequest		true	"The corrected record"
//	@Success		200		{object}	DutyStatusEventResponse	"the new version"
//	@Failure		400		{object}	apierror.Response		"invalid id, status, time or reason, or nothing to change"
//	@Failure		401		{object}	apierror.Response		"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response		"no such record of the caller"
//	@Failure		409		{object}	apierror.Response		"a driving record, would change driving, already replaced, or a record starts at that moment"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status/records/{id} [put]
func (h *DutyStatusHandler) ChangeMyRecord(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	in, ok := bindDutyRecord(c)
	if !ok {
		return
	}
	e, err := h.svc.ChangeMyRecord(c.Request.Context(), p, id, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDutyStatusEventResponse(e))
}

// CertifyMyLog handles POST /api/v1/drivers/me/logs/:date/certification.
//
//	@Summary		Certify my log of a day
//	@Description	The driver signs the day: "my record of duty status for this 24-hour period is true and correct". 201 when signed now; 200 when it was signed and has not changed since. A day changed after its signature must be signed again. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Produce		json
//	@Security		BearerAuth
//	@Param			date	path		string				true	"Day, YYYY-MM-DD"	example(2026-11-10)
//	@Success		201		{object}	DailyLogResponse	"signed"
//	@Success		200		{object}	DailyLogResponse	"already signed, unchanged"
//	@Failure		400		{object}	apierror.Response	"invalid or future date, or nothing recorded"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/logs/{date}/certification [post]
func (h *DutyStatusHandler) CertifyMyLog(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	l, certified, err := h.svc.CertifyMyLog(c.Request.Context(), p, c.Param("date"))
	if err != nil {
		handleError(c, err)
		return
	}
	code := http.StatusOK
	if certified {
		code = http.StatusCreated
	}
	c.JSON(code, newDailyLogResponse(l))
}
