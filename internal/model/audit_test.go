package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuditChanges_Set(t *testing.T) {
	plate, samePlate, newPlate := "ABC", "ABC", "XYZ"
	c := AuditChanges{}

	c.Set("unit_number", "101", "101")         // unchanged: skipped
	c.Set("license_plate", &plate, &samePlate) // same content, different pointers: skipped
	c.Set("make", "Volvo", "Freightliner")
	c.Set("plate", &plate, &newPlate)
	c.Set("year", nil, ptrTo(2022))
	c.Set("model", ptrTo("VNL"), (*string)(nil))
	c.Set("roles", []string{"admin"}, []string{"admin"})

	assert.Equal(t, AuditChanges{
		"make":  {Old: "Volvo", New: "Freightliner"},
		"plate": {Old: "ABC", New: "XYZ"}, // values, not pointers
		"year":  {Old: nil, New: 2022},
		"model": {Old: "VNL", New: nil},
	}, c)
}

func ptrTo[T any](v T) *T { return &v }
