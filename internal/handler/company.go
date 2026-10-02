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
