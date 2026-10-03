package validation

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidator_NoErrors(t *testing.T) {
	var v Validator
	v.Check(true, "name", "is required")

	assert.NoError(t, v.Err())
}

func TestValidator_CollectsAllFields(t *testing.T) {
	var v Validator
	v.Check(false, "name", "is required")
	v.Check(false, "name", "second message is ignored")
	v.Check(false, "dot_number", "must be digits")

	err := v.Err()

	var vErr *ValidationError
	require.True(t, errors.As(err, &vErr))
	assert.Equal(t, map[string]string{
		"name":       "is required",
		"dot_number": "must be digits",
	}, vErr.Fields)
	assert.Equal(t, "validation failed: dot_number: must be digits; name: is required", err.Error())
}
