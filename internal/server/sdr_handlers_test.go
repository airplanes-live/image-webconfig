package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSDRList_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/sdr")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSDRList_ReturnsDetectedDevices(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/sdr")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Devices []struct {
			Serial    string `json:"serial"`
			Product   string `json:"product"`
			BusPath   string `json:"bus_path"`
			Duplicate bool   `json:"duplicate"`
		} `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 1 {
		t.Fatalf("devices = %+v, want the single fixture device", got.Devices)
	}
	d := got.Devices[0]
	if d.Serial != "1090" || d.Product != "RTL2838UHIDIR" || d.BusPath != "1-1.2" || d.Duplicate {
		t.Fatalf("device = %+v", d)
	}
}
