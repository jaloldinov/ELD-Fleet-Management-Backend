package handler

import (
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// DTOs (Data Transfer Objects) define the public JSON contract of the API.
// They are separate from model.Company so the database/domain can change
// without silently changing what clients receive, and so internal fields
// (like deleted_at) can never leak.

type CreateCompanyRequest struct {
	Name      string `json:"name" example:"Acme Freight LLC"`
	DOTNumber string `json:"dot_number" example:"1234567"`
	Timezone  string `json:"timezone" example:"America/Chicago"`
}

func (r CreateCompanyRequest) toInput() service.CreateCompanyInput {
	return service.CreateCompanyInput{Name: r.Name, DOTNumber: r.DOTNumber, Timezone: r.Timezone}
}

// UpdateCompanyRequest is a partial update (PATCH). Pointer fields
// distinguish "not sent" (nil) from "sent as empty/false".
type UpdateCompanyRequest struct {
	Name      *string `json:"name,omitempty" example:"Acme Freight LLC"`
	DOTNumber *string `json:"dot_number,omitempty" example:"1234567"`
	Timezone  *string `json:"timezone,omitempty" example:"America/New_York"`
	IsActive  *bool   `json:"is_active,omitempty" example:"false"`
}

func (r UpdateCompanyRequest) toInput() service.UpdateCompanyInput {
	return service.UpdateCompanyInput{
		Name: r.Name, DOTNumber: r.DOTNumber, Timezone: r.Timezone, IsActive: r.IsActive,
	}
}

type CompanyResponse struct {
	ID        uuid.UUID `json:"id" example:"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"`
	Name      string    `json:"name" example:"Acme Freight LLC"`
	DOTNumber string    `json:"dot_number" example:"1234567"`
	Timezone  string    `json:"timezone" example:"America/Chicago"`
	IsActive  bool      `json:"is_active" example:"true"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T11:26:26Z"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T11:26:26Z"`
}

func newCompanyResponse(c *model.Company) CompanyResponse {
	return CompanyResponse{
		ID:        c.ID,
		Name:      c.Name,
		DOTNumber: c.DOTNumber,
		Timezone:  c.Timezone,
		IsActive:  c.IsActive,
		CreatedAt: c.CreatedAt.UTC(), // API always speaks UTC
		UpdatedAt: c.UpdatedAt.UTC(),
	}
}

type ListCompaniesResponse struct {
	Items    []CompanyResponse `json:"items"`
	Page     int               `json:"page" example:"1"`
	PageSize int               `json:"page_size" example:"20"`
	Total    int               `json:"total" example:"57"`
}

func newListCompaniesResponse(p *service.CompanyPage) ListCompaniesResponse {
	// make(..., 0, n) so an empty page is encoded as [] instead of null.
	items := make([]CompanyResponse, 0, len(p.Items))
	for i := range p.Items {
		items = append(items, newCompanyResponse(&p.Items[i]))
	}
	return ListCompaniesResponse{Items: items, Page: p.Page, PageSize: p.PageSize, Total: p.Total}
}
