package logger

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ProductionWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, true, "info")

	log.Info("hello", "company_id", "abc")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry), "output must be valid JSON")
	assert.Equal(t, "INFO", entry["level"])
	assert.Equal(t, "hello", entry["msg"])
	assert.Equal(t, "abc", entry["company_id"])
}

func TestNew_DevelopmentWritesText(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, false, "info")

	log.Info("hello", "company_id", "abc")

	assert.Contains(t, buf.String(), `msg=hello`)
	assert.Contains(t, buf.String(), `company_id=abc`)
}

func TestNew_LevelFiltersLowerMessages(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, true, "warn")

	log.Info("ignored")
	log.Warn("kept")

	assert.NotContains(t, buf.String(), "ignored")
	assert.Contains(t, buf.String(), "kept")
}
