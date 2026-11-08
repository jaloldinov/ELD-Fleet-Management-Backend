package eldsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Reading is one data point, in the API's JSON shape.
type Reading struct {
	Seq         int64     `json:"seq"`
	RecordedAt  time.Time `json:"recorded_at"`
	Latitude    *float64  `json:"latitude,omitempty"`
	Longitude   *float64  `json:"longitude,omitempty"`
	SpeedMPH    float64   `json:"speed_mph"`
	OdometerMi  float64   `json:"odometer_mi"`
	EngineHours float64   `json:"engine_hours"`
	EngineOn    bool      `json:"engine_on"`
}

// SendResult is the API's answer to a batch.
type SendResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   []struct {
		Index  int    `json:"index"`
		Seq    *int64 `json:"seq"`
		Reason string `json:"reason"`
	} `json:"rejected"`
}

// Me is what the API says about the device.
type Me struct {
	DeviceID     string `json:"device_id"`
	SerialNumber string `json:"serial_number"`
	Truck        *struct {
		ID         string `json:"id"`
		UnitNumber string `json:"unit_number"`
	} `json:"truck"`
	InstalledAt *time.Time `json:"installed_at"` // when it went into Truck
}

// ErrFatal wraps answers a device must not retry: a bad or revoked key
// (401), a retired device (403), a request the API cannot use (400).
// Anything else (network down, 5xx) is temporary: keep the readings, try
// again later.
var ErrFatal = errors.New("the API refused the device")

// Client talks to the API as a device ("Authorization: Device <key>").
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func (c *Client) Me(ctx context.Context) (*Me, error) {
	var me Me
	return &me, c.call(ctx, http.MethodGet, "/api/v1/device/me", nil, &me)
}

// Send posts a batch. sentAt is the device's clock now: the API measures
// the clock's drift from it, and refuses readings recorded after it.
func (c *Client) Send(ctx context.Context, sentAt time.Time, batch []Reading) (*SendResult, error) {
	var res SendResult
	body := map[string]any{"sent_at": sentAt.UTC(), "events": batch}
	return &res, c.call(ctx, http.MethodPost, "/api/v1/device/telemetry", body, &res)
}

func (c *Client) Heartbeat(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/api/v1/device/heartbeat", nil, nil)
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Device "+c.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err) // network: temporary
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s %s: %d %s", ErrFatal, method, path, resp.StatusCode, data)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, data) // temporary
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
