package requestid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "abc-123")

	assert.Equal(t, "abc-123", FromContext(ctx))
}

func TestFromContext_Missing(t *testing.T) {
	assert.Equal(t, "", FromContext(context.Background()))
}
