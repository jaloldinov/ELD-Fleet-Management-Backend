package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDutyStatus_Valid(t *testing.T) {
	for _, s := range []DutyStatus{DutyOffDuty, DutySleeperBerth, DutyDriving, DutyOnDuty} {
		assert.True(t, s.Valid(), s)
	}
	assert.False(t, DutyStatus("").Valid())
	assert.False(t, DutyStatus("OFF").Valid(), "names, not the paper-log abbreviations")
}

func TestSpecialDriving_AllowedWith(t *testing.T) {
	assert.True(t, SpecialPersonalConveyance.AllowedWith(DutyOffDuty))
	assert.False(t, SpecialPersonalConveyance.AllowedWith(DutyOnDuty))
	assert.True(t, SpecialYardMoves.AllowedWith(DutyOnDuty))
	assert.False(t, SpecialYardMoves.AllowedWith(DutyDriving), "yard moves are on duty, not driving")
	assert.False(t, SpecialDriving("towing").AllowedWith(DutyOnDuty))
}
