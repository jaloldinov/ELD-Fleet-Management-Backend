package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmail(t *testing.T) {
	assert.True(t, Email("jane@acme.com"))

	for _, bad := range []string{
		"",
		"jane",
		"jane@localhost",                   // domain needs a dot
		"Jane <jane@acme.com>",             // display-name form
		" jane@acme.com",                   // surrounding space
		strings.Repeat("a", 250) + "@a.co", // too long
	} {
		assert.False(t, Email(bad), bad)
	}
}

func TestTimezone(t *testing.T) {
	assert.True(t, Timezone("America/Chicago"))
	assert.True(t, Timezone("UTC"))

	for _, bad := range []string{"", "Local", "Mars/Olympus"} {
		assert.False(t, Timezone(bad), bad)
	}
}

func TestUSState(t *testing.T) {
	assert.True(t, USState("TX"))
	assert.True(t, USState("DC"))

	for _, bad := range []string{"", "tx", "XX", "TEX", "ON"} {
		assert.False(t, USState(bad), bad)
	}
}
