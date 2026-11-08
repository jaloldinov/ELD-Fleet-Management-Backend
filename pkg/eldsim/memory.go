package eldsim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Memory is what a device keeps across restarts (its flash memory): the
// last seq it used, and the truck's odometer and engine hours. Without it,
// a second run would start again at seq 1, and the API would answer
// "duplicate" to every reading.
type Memory struct {
	LastSeq     int64   `json:"last_seq"`
	OdometerMi  float64 `json:"odometer_mi"`
	EngineHours float64 `json:"engine_hours"`
	// When the last reading was recorded. A new trip must start after it:
	// replaying into time already covered would give later seqs earlier
	// times, which the API refuses (the clock "went backwards").
	LastRecordedAt time.Time `json:"last_recorded_at,omitzero"`
}

// LoadMemory reads path; a missing file is a new device (with a used
// truck: odometer and engine hours start realistic, not at zero).
func LoadMemory(path string) (*Memory, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Memory{OdometerMi: 120000, EngineHours: 8000}, nil
	} else if err != nil {
		return nil, err
	}
	var m Memory
	return &m, json.Unmarshal(data, &m)
}

// Save writes atomically: a temporary file renamed over the old one, so a
// crash mid-write never leaves a half-written memory.
func (m *Memory) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
