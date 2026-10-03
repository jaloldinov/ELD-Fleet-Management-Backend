package vin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The valid examples were verified with an independent implementation
// before being used here: test data must be known-correct.
func TestValid(t *testing.T) {
	tests := []struct {
		vin  string
		want bool
		why  string
	}{
		{"1M8GDM9AXKP042788", true, "check digit X (remainder 10)"},
		{"1HGCM82633A004352", true, "check digit 3"},
		{"11111111111111111", true, "all ones: check digit 1"},

		{"1M8GDM9A1KP042788", false, "wrong check digit"},
		{"1HGCM82633A004353", false, "last character changed: check digit no longer matches"},
		{"1HGCM82633A00435", false, "16 characters"},
		{"1HGCM82633A0043521", false, "18 characters"},
		{"1HGCM8263IA004352", false, "contains I"},
		{"1HGCM8263OA004352", false, "contains O"},
		{"1HGCM8263QA004352", false, "contains Q"},
		{"1hgcm82633a004352", false, "lower case: Normalize first"},
		{"", false, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.why, func(t *testing.T) {
			assert.Equal(t, tt.want, Valid(tt.vin), tt.vin)
		})
	}
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "1HGCM82633A004352", Normalize("  1hgcm82633a004352 "))
	assert.True(t, Valid(Normalize(" 1hgcm82633a004352")))
}
