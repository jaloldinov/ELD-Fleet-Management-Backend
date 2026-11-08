package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// ELDDeviceService is what ELDDeviceHandler needs from the service layer.
type ELDDeviceService interface {
	Create(ctx context.Context, p auth.Principal, in service.CreateELDDeviceInput) (*model.ELDDevice, error)
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error)
	List(ctx context.Context, p auth.Principal, params service.ELDDeviceListParams) (*service.ELDDevicePage, error)
	Update(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdateELDDeviceInput) (*model.ELDDevice, error)
	Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error
	IssueKey(ctx context.Context, p auth.Principal, id uuid.UUID) (string, *model.ELDDevice, error)
	RevokeKey(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, error)
	State(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.ELDDevice, *model.DeviceState, error)
}

// DeviceStateResponse: is the device talking, and what did it last say.
type DeviceStateResponse struct {
	DeviceID   uuid.UUID         `json:"device_id" swaggertype:"string" format:"uuid"`
	Connection string            `json:"connection" enums:"online,offline,never" example:"online"`
	LastSeenAt *time.Time        `json:"last_seen_at" example:"2026-11-08T14:31:00Z"`
	TruckID    *uuid.UUID        `json:"truck_id" swaggertype:"string" format:"uuid"` // installed in now
	Position   *PositionResponse `json:"position"`                                    // null = no reading yet
	// How far the device's clock is from ours (positive = fast), from the
	// last batch that carried sent_at; null = never measured. FMCSA allows
	// 10 minutes (600 s); readings beyond that are refused.
	ClockOffsetSeconds *float64   `json:"clock_offset_seconds" example:"-2.5"`
	ClockCheckedAt     *time.Time `json:"clock_checked_at" example:"2026-11-08T14:31:00Z"`
}

// ---------- DTOs ----------

type CreateELDDeviceRequest struct {
	// Platform users: required. Company users: omit (their own company is used).
	CompanyID *uuid.UUID `json:"company_id,omitempty" swaggertype:"string" format:"uuid"`
	// Hardware serial, 4-32 letters/digits/hyphens. Cannot be changed later.
	SerialNumber    string `json:"serial_number" example:"PT30-0001"`
	Model           string `json:"model,omitempty" example:"PT30"`
	FirmwareVersion string `json:"firmware_version,omitempty" example:"1.2.3"`
}

// UpdateELDDeviceRequest: omitted fields are unchanged. The serial number
// cannot be changed; the truck is changed with PUT/DELETE .../truck
// (AssignmentHandler).
type UpdateELDDeviceRequest struct {
	Model           *string `json:"model,omitempty" example:"PT30"`
	FirmwareVersion *string `json:"firmware_version,omitempty" example:"2.0.0"`
	// active: works. faulty: broken, may stay in its truck until replaced.
	// retired: end of life, must be taken out of its truck first.
	Status *string `json:"status,omitempty" enums:"active,faulty,retired" example:"faulty"`
	// Older way to set the status: true = active, false = retired.
	IsActive *bool `json:"is_active,omitempty" example:"false"`
}

type ELDDeviceResponse struct {
	ID              uuid.UUID  `json:"id" swaggertype:"string" format:"uuid"`
	CompanyID       uuid.UUID  `json:"company_id" swaggertype:"string" format:"uuid"`
	SerialNumber    string     `json:"serial_number" example:"PT30-0001"`
	Model           string     `json:"model" example:"PT30"`
	FirmwareVersion string     `json:"firmware_version" example:"1.2.3"`
	TruckID         *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"` // null = not installed
	Status          string     `json:"status" enums:"active,faulty,retired" example:"active"`
	IsActive        bool       `json:"is_active" example:"true"` // status is active
	// The device's API key (never the key itself): has_key = the device can
	// call the API; when the key was issued.
	HasKey       bool       `json:"has_key" example:"true"`
	KeyCreatedAt *time.Time `json:"key_created_at" example:"2026-11-08T09:00:00Z"`
	// The device's last request (to the minute); null = never.
	LastSeenAt *time.Time `json:"last_seen_at" example:"2026-11-08T09:42:00Z"`
	CreatedAt  time.Time  `json:"created_at" example:"2026-10-02T11:26:26Z"`
	UpdatedAt  time.Time  `json:"updated_at" example:"2026-10-02T11:26:26Z"`
}

func newELDDeviceResponse(d *model.ELDDevice) ELDDeviceResponse {
	return ELDDeviceResponse{
		ID: d.ID, CompanyID: d.CompanyID, SerialNumber: d.SerialNumber,
		Model: d.Model, FirmwareVersion: d.FirmwareVersion, TruckID: d.TruckID,
		Status: string(d.Status), IsActive: d.IsActive,
		HasKey: d.KeyCreatedAt != nil, KeyCreatedAt: utcOrNil(d.KeyCreatedAt), LastSeenAt: utcOrNil(d.LastSeenAt),
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
	}
}

// IssueKeyResponse carries the new key: shown this once, never again.
type IssueKeyResponse struct {
	// Put it into the device. Only its hash is stored: if it is lost, issue
	// a new one (which also stops this one).
	DeviceKey string            `json:"device_key" example:"eldk_q8Gm0yJ3v1Xp..."`
	ELDDevice ELDDeviceResponse `json:"eld_device"`
}

func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

type ListELDDevicesResponse struct {
	Items    []ELDDeviceResponse `json:"items"`
	Page     int                 `json:"page" example:"1"`
	PageSize int                 `json:"page_size" example:"20"`
	Total    int                 `json:"total" example:"14"`
}

// ---------- Handler ----------

type ELDDeviceHandler struct {
	svc ELDDeviceService
}

func NewELDDeviceHandler(svc ELDDeviceService) *ELDDeviceHandler {
	return &ELDDeviceHandler{svc: svc}
}

// Create handles POST /api/v1/eld-devices.
//
//	@Summary		Register an ELD device
//	@Description	Requires permission: eld:create
//	@Description	New devices are not installed in any truck; use PUT /eld-devices/{id}/truck.
//	@Tags			eld-devices
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateELDDeviceRequest	true	"Device to register"
//	@Success		201		{object}	ELDDeviceResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission eld:create"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		409		{object}	apierror.Response	"serial number already registered"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/eld-devices [post]
func (h *ELDDeviceHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req CreateELDDeviceRequest
	if !bindJSON(c, &req) {
		return
	}

	d, err := h.svc.Create(c.Request.Context(), p, service.CreateELDDeviceInput{
		CompanyID: req.CompanyID, SerialNumber: req.SerialNumber,
		Model: req.Model, FirmwareVersion: req.FirmwareVersion,
	})
	if err != nil {
		handleError(c, err)
		return
	}

	c.Header("Location", "/api/v1/eld-devices/"+d.ID.String())
	c.JSON(http.StatusCreated, newELDDeviceResponse(d))
}

// List handles GET /api/v1/eld-devices.
//
//	@Summary		List ELD devices
//	@Description	Requires permission: eld:read
//	@Description	Company users always get their own company's devices (company_id is ignored).
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id	query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			is_active	query		bool	false	"Only active (true) or inactive (false) devices"
//	@Param			status		query		string	false	"Only devices with this status"			Enums(active, faulty, retired)
//	@Param			truck_id	query		string	false	"The device installed in this truck"	format(uuid)
//	@Param			page		query		int		false	"Page number (1-based)"					default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"						default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListELDDevicesResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission eld:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/eld-devices [get]
func (h *ELDDeviceHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var v validation.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	companyID, ok := queryUUID(c, "company_id")
	v.Check(ok, "company_id", "must be a valid UUID")
	truckID, ok := queryUUID(c, "truck_id")
	v.Check(ok, "truck_id", "must be a valid UUID")
	var isActive *bool
	if s := c.Query("is_active"); s != "" {
		b, err := strconv.ParseBool(s)
		v.Check(err == nil, "is_active", "must be true or false")
		isActive = &b
	}
	var status *model.ELDStatus
	if s := c.Query("status"); s != "" {
		st := model.ELDStatus(s)
		v.Check(st.Valid(), "status", "must be active, faulty or retired")
		status = &st
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.List(c.Request.Context(), p, service.ELDDeviceListParams{
		CompanyID: companyID, IsActive: isActive, Status: status, TruckID: truckID, Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]ELDDeviceResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newELDDeviceResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListELDDevicesResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// Get handles GET /api/v1/eld-devices/:id.
//
//	@Summary		Get an ELD device
//	@Description	Requires permission: eld:read
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Device ID"	format(uuid)
//	@Success		200	{object}	ELDDeviceResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id} [get]
func (h *ELDDeviceHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.Get(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newELDDeviceResponse(d))
}

// Update handles PATCH /api/v1/eld-devices/:id.
//
//	@Summary		Update an ELD device
//	@Description	Requires permission: eld:update
//	@Description	Partial update. The serial number cannot be changed. An installed device cannot be deactivated.
//	@Tags			eld-devices
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string					true	"Device ID"	format(uuid)
//	@Param			body	body		UpdateELDDeviceRequest	true	"Fields to change"
//	@Success		200		{object}	ELDDeviceResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission eld:update"
//	@Failure		404		{object}	apierror.Response
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id} [patch]
func (h *ELDDeviceHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateELDDeviceRequest
	if !bindJSON(c, &req) {
		return
	}

	d, err := h.svc.Update(c.Request.Context(), p, id, service.UpdateELDDeviceInput{
		Model: req.Model, FirmwareVersion: req.FirmwareVersion, IsActive: req.IsActive,
		Status: (*model.ELDStatus)(req.Status),
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newELDDeviceResponse(d))
}

// IssueKey handles POST /api/v1/eld-devices/:id/credentials.
//
//	@Summary		Issue a device API key
//	@Description	Requires permission: eld:update
//	@Description	Returns a new key for the device to send as "Authorization: Device <key>". It is shown ONCE: only its hash is stored. Issuing again rotates: the old key stops working at once.
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Device ID"	format(uuid)
//	@Success		201	{object}	IssueKeyResponse
//	@Failure		400	{object}	apierror.Response	"the device is retired"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:update"
//	@Failure		404	{object}	apierror.Response	"device not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id}/credentials [post]
func (h *ELDDeviceHandler) IssueKey(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	key, d, err := h.svc.IssueKey(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	// Secrets must not be cached by browsers or proxies.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, IssueKeyResponse{DeviceKey: key, ELDDevice: newELDDeviceResponse(d)})
}

// RevokeKey handles DELETE /api/v1/eld-devices/:id/credentials.
//
//	@Summary		Revoke a device API key
//	@Description	Requires permission: eld:update
//	@Description	The device can no longer call the API until a new key is issued. Succeeds also when the device has no key.
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Device ID"	format(uuid)
//	@Success		200	{object}	ELDDeviceResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:update"
//	@Failure		404	{object}	apierror.Response	"device not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id}/credentials [delete]
func (h *ELDDeviceHandler) RevokeKey(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.RevokeKey(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newELDDeviceResponse(d))
}

// State handles GET /api/v1/eld-devices/:id/state.
//
//	@Summary		A device's last known state
//	@Description	Requires permission: eld:read
//	@Description	Whether the device is online (heard from within 5 minutes) and its newest reading: position, speed, moving, engine, odometer. Newest by the device's clock: readings sent late after being offline never replace a newer one.
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Device ID"	format(uuid)
//	@Success		200	{object}	DeviceStateResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:read"
//	@Failure		404	{object}	apierror.Response	"device not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id}/state [get]
func (h *ELDDeviceHandler) State(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, st, err := h.svc.State(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	r := DeviceStateResponse{DeviceID: d.ID,
		Connection: string(model.ConnectionOf(d.LastSeenAt, time.Now())), LastSeenAt: utcOrNil(d.LastSeenAt),
		TruckID: d.TruckID, Position: newPositionResponse(st), ClockCheckedAt: utcOrNil(d.ClockCheckedAt)}
	if d.ClockOffset != nil {
		sec := d.ClockOffset.Seconds()
		r.ClockOffsetSeconds = &sec
	}
	c.JSON(http.StatusOK, r)
}

// Delete handles DELETE /api/v1/eld-devices/:id.
//
//	@Summary		Delete an ELD device
//	@Description	Requires permission: eld:delete
//	@Description	Soft delete; also removes the device from its truck. The serial can be registered again afterwards.
//	@Tags			eld-devices
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Device ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:delete"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id} [delete]
func (h *ELDDeviceHandler) Delete(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), p, id); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
