package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestPrincipal_RoundTrip(t *testing.T) {
	companyID := uuid.New()
	p := Principal{UserID: uuid.New(), CompanyID: &companyID}

	got, ok := PrincipalFromContext(WithPrincipal(context.Background(), p))

	assert.True(t, ok)
	assert.Equal(t, p, got)
	assert.False(t, got.IsPlatformUser())
}

func TestPrincipal_Missing(t *testing.T) {
	_, ok := PrincipalFromContext(context.Background())

	assert.False(t, ok)
}

func TestPrincipal_PlatformUser(t *testing.T) {
	assert.True(t, Principal{UserID: uuid.New()}.IsPlatformUser())
}

func TestSystemPrincipal(t *testing.T) {
	p := SystemPrincipal()

	assert.True(t, p.IsPlatformUser())
	assert.Equal(t, uuid.Nil, p.UserID, "not a real user")
}
