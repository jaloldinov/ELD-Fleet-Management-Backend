package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- Phase 3 response shapes (the JSON contract) ----------

type stintResp struct {
	ID        string  `json:"id"`
	DriverID  string  `json:"driver_id"`
	TruckID   string  `json:"truck_id"`
	Role      string  `json:"role"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at"`
	StartedBy *string `json:"started_by"`
	EndedBy   *string `json:"ended_by"`
}

type boardResp struct {
	Truck struct {
		ID         string `json:"id"`
		UnitNumber string `json:"unit_number"`
	} `json:"truck"`
	Drivers []struct {
		DriverID  string `json:"driver_id"`
		FirstName string `json:"first_name"`
		Role      string `json:"role"`
	} `json:"drivers"`
	ELDDevice *struct {
		ID           string `json:"id"`
		SerialNumber string `json:"serial_number"`
	} `json:"eld_device"`
}

type driverTruckResp struct {
	DriverID string     `json:"driver_id"`
	Role     *string    `json:"role"`
	Truck    *boardResp `json:"truck"`
}

type stintList struct {
	page
	Items []struct {
		stintResp
		TruckUnitNumber string `json:"truck_unit_number"`
		DriverFirstName string `json:"driver_first_name"`
	} `json:"items"`
}

type eldStintList struct {
	page
	Items []struct {
		ELDDeviceID string  `json:"eld_device_id"`
		TruckID     string  `json:"truck_id"`
		EndedAt     *string `json:"ended_at"`
	} `json:"items"`
}

// TestDispatchJourney walks Phase 3 through the running API: a dispatch
// office's week. Drivers are put on trucks (by the office and by
// themselves), moved, see their truck and co-driver; trucks go to the
// shop and out of service; a broken ELD is replaced; dispatchers come and
// go; and the history answers "who drove which truck when", also after the
// truck is deleted. Steps share state, so a failed step stops the journey.
func TestDispatchJourney(t *testing.T) {
	tn := newTenant(t)
	dispUser, disp := tn.newUser(t, "dispatcher")
	ali, aliC := tn.newDriver(t)
	bob, bobC := tn.newDriver(t)
	cat, catC := tn.newDriver(t)
	t1, t2 := tn.newTruck(t), tn.newTruck(t)
	e1, e2 := tn.newELD(t), tn.newELD(t)

	var aliFirst stintResp // Ali's first stint, for the history questions

	step := func(name string, fn func(t *testing.T)) {
		if !t.Run(name, fn) {
			t.FailNow()
		}
	}
	assign := func(c client, driverID, truckID, role string, want int) stintResp {
		t.Helper()
		body := map[string]any{"truck_id": truckID}
		if role != "" {
			body["role"] = role
		}
		var s stintResp
		r := c.do(t, http.MethodPut, "/api/v1/drivers/"+driverID+"/truck", body, want, nil)
		if want == http.StatusOK {
			r.decode(t, &s)
		}
		return s
	}
	board := func(truckID string) boardResp {
		t.Helper()
		var b boardResp
		disp.do(t, http.MethodGet, "/api/v1/trucks/"+truckID+"/current", nil, http.StatusOK, &b)
		return b
	}

	step("the dispatcher puts Ali on truck 1 (primary by default)", func(t *testing.T) {
		aliFirst = assign(disp, ali.ID, t1.ID, "", http.StatusOK)
		assert.Equal(t, "primary", aliFirst.Role)
		assert.Nil(t, aliFirst.EndedAt, "current stint: explicit null")
		require.NotNil(t, aliFirst.StartedBy)
		assert.Equal(t, dispUser.ID, *aliFirst.StartedBy, "who did it is kept")

		again := assign(disp, ali.ID, t1.ID, "primary", http.StatusOK)
		assert.Equal(t, aliFirst.ID, again.ID, "the same PUT twice changes nothing")
	})

	step("one primary per truck; a co-driver fits", func(t *testing.T) {
		e := disp.fail(t, http.MethodPut, "/api/v1/drivers/"+bob.ID+"/truck",
			map[string]any{"truck_id": t1.ID}, http.StatusConflict)
		assert.Equal(t, "conflict", e.Code)
		assert.Equal(t, "co_driver", assign(disp, bob.ID, t1.ID, "co_driver", http.StatusOK).Role)
	})

	step("a driver picks their own truck, but nobody else's", func(t *testing.T) {
		var s stintResp
		catC.do(t, http.MethodPut, "/api/v1/drivers/me/truck", map[string]any{"truck_id": t2.ID}, http.StatusOK, &s)
		require.NotNil(t, s.StartedBy)
		assert.Equal(t, cat.UserID, *s.StartedBy, "self-service shows in the history")

		catC.fail(t, http.MethodPut, "/api/v1/drivers/"+bob.ID+"/truck", map[string]any{"truck_id": t2.ID}, http.StatusForbidden)
		catC.fail(t, http.MethodGet, "/api/v1/trucks/"+t1.ID+"/current", nil, http.StatusForbidden)
	})

	step("installing a device is a manager's job, not a dispatcher's", func(t *testing.T) {
		disp.fail(t, http.MethodPut, "/api/v1/eld-devices/"+e1.ID+"/truck", map[string]any{"truck_id": t1.ID}, http.StatusForbidden)
		tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+e1.ID+"/truck", map[string]any{"truck_id": t1.ID}, http.StatusOK, nil)
	})

	step("the board shows truck 1's crew and device", func(t *testing.T) {
		b := board(t1.ID)
		assert.Equal(t, t1.UnitNumber, b.Truck.UnitNumber)
		require.Len(t, b.Drivers, 2)
		assert.Equal(t, ali.ID, b.Drivers[0].DriverID, "primary first")
		assert.NotEmpty(t, b.Drivers[0].FirstName, "with names (newDriver calls everyone Bob)")
		assert.Equal(t, "co_driver", b.Drivers[1].Role)
		require.NotNil(t, b.ELDDevice)
		assert.Equal(t, e1.SerialNumber, b.ELDDevice.SerialNumber)
	})

	step("Bob's app shows his truck, his co-driver and the ELD to connect to", func(t *testing.T) {
		var me driverTruckResp
		bobC.do(t, http.MethodGet, "/api/v1/drivers/me/truck", nil, http.StatusOK, &me)
		assert.Equal(t, bob.ID, me.DriverID)
		require.NotNil(t, me.Role)
		assert.Equal(t, "co_driver", *me.Role)
		require.NotNil(t, me.Truck)
		assert.Len(t, me.Truck.Drivers, 2)
		require.NotNil(t, me.Truck.ELDDevice)
		assert.Equal(t, e1.ID, me.Truck.ELDDevice.ID)
	})

	step("moving Ali to truck 2 ends his first stint", func(t *testing.T) {
		assign(disp, ali.ID, t2.ID, "co_driver", http.StatusOK)

		assert.Len(t, board(t1.ID).Drivers, 1, "only Bob is left on truck 1")

		var l stintList
		disp.do(t, http.MethodGet, "/api/v1/driver-assignments?driver_id="+ali.ID, nil, http.StatusOK, &l)
		require.Equal(t, 2, l.Total)
		assert.Equal(t, t2.UnitNumber, l.Items[0].TruckUnitNumber, "newest first")
		assert.Nil(t, l.Items[0].EndedAt)
		assert.Equal(t, aliFirst.ID, l.Items[1].ID)
		assert.NotNil(t, l.Items[1].EndedAt, "the first stint is history now")
	})

	step("history: who was on truck 1 when Ali started there?", func(t *testing.T) {
		var l stintList
		disp.do(t, http.MethodGet, "/api/v1/driver-assignments?truck_id="+t1.ID+"&at="+aliFirst.StartedAt, nil, http.StatusOK, &l)
		require.Equal(t, 1, l.Total, "Bob came later")
		assert.Equal(t, ali.ID, l.Items[0].DriverID)

		e := disp.fail(t, http.MethodGet, "/api/v1/driver-assignments?at=yesterday", nil, http.StatusBadRequest)
		assert.Contains(t, e.Details, "at")
	})

	step("truck 1 goes to the shop: Bob comes off, the ELD stays", func(t *testing.T) {
		var tr struct {
			Status   string `json:"status"`
			IsActive bool   `json:"is_active"`
		}
		tn.Admin.do(t, http.MethodPatch, "/api/v1/trucks/"+t1.ID, map[string]any{"status": "in_maintenance"}, http.StatusOK, &tr)
		assert.Equal(t, "in_maintenance", tr.Status)
		assert.True(t, tr.IsActive, "still part of the fleet")

		b := board(t1.ID)
		assert.Empty(t, b.Drivers)
		require.NotNil(t, b.ELDDevice, "the device stays in the truck")

		var me driverTruckResp
		bobC.do(t, http.MethodGet, "/api/v1/drivers/me/truck", nil, http.StatusOK, &me)
		assert.Nil(t, me.Truck, "Bob is on no truck now")

		e := disp.fail(t, http.MethodPut, "/api/v1/drivers/"+bob.ID+"/truck", map[string]any{"truck_id": t1.ID}, http.StatusBadRequest)
		assert.Equal(t, "truck is in maintenance", e.Details["truck_id"])
	})

	step("a broken ELD is replaced in the shop", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPatch, "/api/v1/eld-devices/"+e1.ID, map[string]any{"status": "faulty"}, http.StatusOK, nil)
		tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+e2.ID+"/truck", map[string]any{"truck_id": t1.ID}, http.StatusConflict)

		var d eldResp
		tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+e2.ID+"/truck",
			map[string]any{"truck_id": t1.ID, "replace": true}, http.StatusOK, &d)
		require.NotNil(t, d.TruckID)
		assert.Equal(t, t1.ID, *d.TruckID)

		var old eldResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+e1.ID, nil, http.StatusOK, &old)
		assert.Nil(t, old.TruckID, "the faulty one came out")

		var l eldStintList
		disp.do(t, http.MethodGet, "/api/v1/eld-assignments?truck_id="+t1.ID, nil, http.StatusOK, &l)
		require.Equal(t, 2, l.Total)
		assert.Equal(t, e2.ID, l.Items[0].ELDDeviceID)
		assert.NotNil(t, l.Items[1].EndedAt)
	})

	step("Cat gets a dispatcher; the dispatcher sees 'my drivers'", func(t *testing.T) {
		var d struct {
			DispatcherID *string `json:"dispatcher_id"`
		}
		tn.Admin.do(t, http.MethodPut, "/api/v1/drivers/"+cat.ID+"/dispatcher",
			map[string]any{"dispatcher_id": dispUser.ID}, http.StatusOK, &d)
		require.NotNil(t, d.DispatcherID)

		var mine struct {
			page
			Items []driverResp `json:"items"`
		}
		disp.do(t, http.MethodGet, "/api/v1/drivers?dispatcher_id=me", nil, http.StatusOK, &mine)
		require.Equal(t, 1, mine.Total)
		assert.Equal(t, cat.ID, mine.Items[0].ID)

		e := tn.Admin.fail(t, http.MethodPut, "/api/v1/drivers/"+cat.ID+"/dispatcher",
			map[string]any{"dispatcher_id": bob.UserID}, http.StatusBadRequest)
		assert.Contains(t, e.Details["dispatcher_id"], "cannot dispatch", "a driver holds no assignment:manage")
	})

	step("the dispatcher leaves: Cat is unlinked", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+dispUser.ID, map[string]any{"is_active": false}, http.StatusOK, nil)

		var d struct {
			DispatcherID *string `json:"dispatcher_id"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+cat.ID, nil, http.StatusOK, &d)
		assert.Nil(t, d.DispatcherID)
	})

	step("Cat's account is disabled: her seat on truck 2 is free", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+cat.UserID, map[string]any{"is_active": false}, http.StatusOK, nil)

		var b boardResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks/"+t2.ID+"/current", nil, http.StatusOK, &b)
		require.Len(t, b.Drivers, 1)
		assert.Equal(t, ali.ID, b.Drivers[0].DriverID)

		e := tn.Admin.fail(t, http.MethodPut, "/api/v1/drivers/"+cat.ID+"/truck", map[string]any{"truck_id": t2.ID}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "driver")
	})

	step("truck 2 leaves the fleet: Ali comes off, ended by the admin", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPatch, "/api/v1/trucks/"+t2.ID, map[string]any{"status": "out_of_service"}, http.StatusOK, nil)

		var me driverTruckResp
		aliC.do(t, http.MethodGet, "/api/v1/drivers/me/truck", nil, http.StatusOK, &me)
		assert.Nil(t, me.Truck)

		var l stintList
		tn.Admin.do(t, http.MethodGet, "/api/v1/driver-assignments?driver_id="+ali.ID, nil, http.StatusOK, &l)
		require.NotNil(t, l.Items[0].EndedBy)
		assert.Equal(t, tn.AdminID, *l.Items[0].EndedBy)
	})

	step("another company sees none of it", func(t *testing.T) {
		other := newTenant(t)
		other.Admin.fail(t, http.MethodGet, "/api/v1/trucks/"+t1.ID+"/current", nil, http.StatusNotFound)

		var l stintList
		other.Admin.do(t, http.MethodGet, "/api/v1/driver-assignments?truck_id="+t1.ID, nil, http.StatusOK, &l)
		assert.Zero(t, l.Total, "another company's truck id simply matches nothing")

		theirDriver, _ := other.newDriver(t)
		e := other.Admin.fail(t, http.MethodPut, "/api/v1/drivers/"+theirDriver.ID+"/truck",
			map[string]any{"truck_id": t1.ID}, http.StatusBadRequest)
		assert.Equal(t, "truck not found", e.Details["truck_id"], "the same answer as for a missing truck")
	})

	step("a deleted truck keeps its history", func(t *testing.T) {
		tn.Admin.do(t, http.MethodDelete, "/api/v1/trucks/"+t1.ID, nil, http.StatusNoContent, nil)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/trucks/"+t1.ID+"/current", nil, http.StatusNotFound)

		var l stintList
		tn.Admin.do(t, http.MethodGet, "/api/v1/driver-assignments?truck_id="+t1.ID, nil, http.StatusOK, &l)
		assert.Equal(t, 2, l.Total, "Ali's and Bob's stints are still there")
		assert.Equal(t, t1.UnitNumber, l.Items[0].TruckUnitNumber)

		var el eldStintList
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-assignments?truck_id="+t1.ID, nil, http.StatusOK, &el)
		assert.Equal(t, 2, el.Total)
		assert.NotNil(t, el.Items[0].EndedAt, "deleting the truck took the device out")
	})
}
