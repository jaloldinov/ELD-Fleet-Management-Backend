package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryPoint_Moving(t *testing.T) {
	assert.False(t, TelemetryPoint{SpeedMPH: 0}.Moving())
	assert.False(t, TelemetryPoint{SpeedMPH: 5}.Moving(), "5 mph exactly is not moving")
	assert.True(t, TelemetryPoint{SpeedMPH: 5.1}.Moving())
}

func TestConnectionOf(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	assert.Equal(t, ConnectionNever, ConnectionOf(nil, now))
	assert.Equal(t, ConnectionOnline, ConnectionOf(at(30*time.Second), now))
	assert.Equal(t, ConnectionOnline, ConnectionOf(at(5*time.Minute), now), "exactly 5 minutes: still online")
	assert.Equal(t, ConnectionOffline, ConnectionOf(at(5*time.Minute+time.Second), now))
}
