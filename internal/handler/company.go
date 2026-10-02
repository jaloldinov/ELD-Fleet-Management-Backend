package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// CompanyService is what CompanyHandler needs from the service layer.
// *service.CompanyService satisfies it; tests use a fake.
type CompanyService interface {
	Create(ctx context.Context, in service.CreateCompanyInput) (*model.Company, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
	List(ctx context.Context, p service.ListParams) (*service.CompanyPage, error)
	Update(ctx context.Context, id uuid.UUID, in service.UpdateCompanyInput) (*model.Company, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type CompanyHandler struct {
	svc CompanyService
}

func NewCompanyHandler(svc CompanyService) *CompanyHandler {
	return &CompanyHandler{svc: svc}
}

// Create handles POST /api/v1/companies.
//
//	@Summary		Create a company
//	@Description	Creates a motor carrier (tenant). New companies are active.
//	@Tags			companies
//	@Accept			json
//	@Produce		json
//	@Param			body	body		CreateCompanyRequest	true	"Company to create"
//	@Success		201		{object}	CompanyResponse
//	@Header			201		{string}	Location		"URL of the created company"
//	@Failure		400		{object}	ErrorResponse	"invalid JSON or validation failed"
//	@Failure		409		{object}	ErrorResponse	"DOT number already used"
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/companies [post]
func (h *CompanyHandler) Create(c *gin.Context) {
	var req CreateCompanyRequest
	if !bindJSON(c, &req) {
		return
	}

	// c.Request.Context() carries the request ID and is cancelled if the
	// client disconnects, so the service/database can stop early.
	company, err := h.svc.Create(c.Request.Context(), req.toInput())
	if err != nil {
		handleError(c, err)
		return
	}

	// REST convention: 201 Created + Location header pointing to the new resource.
	c.Header("Location", "/api/v1/companies/"+company.ID.String())
	c.JSON(http.StatusCreated, newCompanyResponse(company))
}

// Get handles GET /api/v1/companies/:id.
//
//	@Summary	Get a company
//	@Tags		companies
//	@Produce	json
//	@Param		id	path		string	true	"Company ID"	format(uuid)
//	@Success	200	{object}	CompanyResponse
//	@Failure	400	{object}	ErrorResponse	"invalid id"
//	@Failure	404	{object}	ErrorResponse
//	@Failure	500	{object}	ErrorResponse
//	@Router		/api/v1/companies/{id} [get]
func (h *CompanyHandler) Get(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	company, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		handleError(c, err)
		return
	}

	c.JSON(http.StatusOK, newCompanyResponse(company))
}

// List handles GET /api/v1/companies?page=1&page_size=20.
//
//	@Summary		List companies
//	@Description	Returns companies ordered by creation time. page_size is capped at 100.
//	@Tags			companies
//	@Produce		json
//	@Param			page		query		int	false	"Page number (1-based)"	default(1)	minimum(1)
//	@Param			page_size	query		int	false	"Items per page"		default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListCompaniesResponse
//	@Failure		400			{object}	ErrorResponse
//	@Failure		500			{object}	ErrorResponse
//	@Router			/api/v1/companies [get]
func (h *CompanyHandler) List(c *gin.Context) {
	var v model.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	result, err := h.svc.List(c.Request.Context(), service.ListParams{Page: page, PageSize: size})
	if err != nil {
		handleError(c, err)
		return
	}

	c.JSON(http.StatusOK, newListCompaniesResponse(result))
}

// Update handles PATCH /api/v1/companies/:id.
//
//	@Summary		Update a company
//	@Description	Partial update: only the fields present in the body are changed.
//	@Tags			companies
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string					true	"Company ID"	format(uuid)
//	@Param			body	body		UpdateCompanyRequest	true	"Fields to change"
//	@Success		200		{object}	CompanyResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		404		{object}	ErrorResponse
//	@Failure		409		{object}	ErrorResponse	"DOT number already used"
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/companies/{id} [patch]
func (h *CompanyHandler) Update(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateCompanyRequest
	if !bindJSON(c, &req) {
		return
	}

	company, err := h.svc.Update(c.Request.Context(), id, req.toInput())
	if err != nil {
		handleError(c, err)
		return
	}

	c.JSON(http.StatusOK, newCompanyResponse(company))
}

// Delete handles DELETE /api/v1/companies/:id (soft delete).
//
//	@Summary		Delete a company
//	@Description	Soft delete: the company is hidden but kept for history.
//	@Tags			companies
//	@Param			id	path	string	true	"Company ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Failure		500	{object}	ErrorResponse
//	@Router			/api/v1/companies/{id} [delete]
func (h *CompanyHandler) Delete(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		handleError(c, err)
		return
	}

	c.Status(http.StatusNoContent) // success, nothing to return
}
