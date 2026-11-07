package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAssignmentRole_Valid(t *testing.T) {
	assert.True(t, AssignmentPrimary.Valid())
	assert.True(t, AssignmentCoDriver.Valid())
	assert.False(t, AssignmentRole("passenger").Valid())
	assert.False(t, AssignmentRole("").Valid())
}

func TestDriverAssignment_IsCurrent(t *testing.T) {
	assert.True(t, DriverAssignment{}.IsCurrent())
	ended := time.Now()
	assert.False(t, DriverAssignment{EndedAt: &ended}.IsCurrent())
}
