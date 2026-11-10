package service

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Guard test (no database, no Stripe): the events Stripe is told to send
// (STRIPE_EVENTS in the Makefile, used by `make stripe-listen` and
// `make stripe-webhook-endpoint`) must be exactly the events webhook.go
// handles. An event handled but not listed is never delivered, silently;
// one listed but not handled is delivered for nothing.
func TestWebhookEvents_MakefileMatchesHandler(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	require.NoError(t, err)
	line := regexp.MustCompile(`(?m)^STRIPE_EVENTS := (.+)$`).FindSubmatch(makefile)
	require.NotNil(t, line, "STRIPE_EVENTS not found in the Makefile")
	listed := strings.Split(strings.TrimSpace(string(line[1])), ",")

	src, err := os.ReadFile("webhook.go")
	require.NoError(t, err)
	var handled []string
	for _, m := range regexp.MustCompile(`"((?:customer\.subscription|invoice)\.[a-z_]+)"`).FindAllSubmatch(src, -1) {
		if e := string(m[1]); !slices.Contains(handled, e) {
			handled = append(handled, e)
		}
	}

	slices.Sort(listed)
	slices.Sort(handled)
	assert.Equal(t, handled, listed, "STRIPE_EVENTS (Makefile) and the events in webhook.go must match")
	assert.Len(t, listed, 13)
}
