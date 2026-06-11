package sdr

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDevice lays out a fake sysfs USB device directory. Empty attribute
// values are written as empty files (sysfs would not do that, but the
// reader must tolerate it); a "-" value means "omit the file entirely".
func writeDevice(t *testing.T, root, busPath string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, busPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, val := range attrs {
		if val == "-" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestList_MissingRootIsEmptyNotError(t *testing.T) {
	t.Parallel()
	got := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if got == nil {
		t.Fatal("List returned nil; want non-nil empty slice for clean JSON")
	}
	if len(got) != 0 {
		t.Fatalf("List = %v, want empty", got)
	}
}

func TestList_FiltersByKnownVidPid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The SDR.
	writeDevice(t, root, "1-1.2", map[string]string{
		"idVendor": "0bda", "idProduct": "2838", "serial": "1090", "product": "RTL2838UHIDIR",
	})
	// Realtek Wi-Fi dongle: same vendor, unknown product id — must be excluded.
	writeDevice(t, root, "1-1.3", map[string]string{
		"idVendor": "0bda", "idProduct": "8179", "serial": "00WIFI", "product": "802.11n NIC",
	})
	// Non-Realtek unrelated device.
	writeDevice(t, root, "1-1.4", map[string]string{
		"idVendor": "046d", "idProduct": "c52b", "serial": "X", "product": "USB Receiver",
	})
	// Interface-style entry without attribute files.
	writeDevice(t, root, "1-1:1.0", map[string]string{"idVendor": "-", "idProduct": "-"})

	got := List(root)
	if len(got) != 1 {
		t.Fatalf("List = %+v, want exactly the RTL2838 entry", got)
	}
	d := got[0]
	if d.Serial != "1090" || d.Product != "RTL2838UHIDIR" || d.BusPath != "1-1.2" || d.Duplicate {
		t.Fatalf("device = %+v", d)
	}
}

func TestList_SkipsEmptyAndMissingSerials(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDevice(t, root, "1-1.2", map[string]string{
		"idVendor": "0bda", "idProduct": "2838", "serial": "",
	})
	writeDevice(t, root, "1-1.3", map[string]string{
		"idVendor": "0bda", "idProduct": "2832", "serial": "-",
	})
	if got := List(root); len(got) != 0 {
		t.Fatalf("List = %+v, want empty (no usable serials)", got)
	}
}

func TestList_MarksDuplicatesAndSortsByBusPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Two factory-default serials plus one distinct, written out of order.
	writeDevice(t, root, "1-1.4", map[string]string{
		"idVendor": "0bda", "idProduct": "2838", "serial": "00000001",
	})
	writeDevice(t, root, "1-1.2", map[string]string{
		"idVendor": "0bda", "idProduct": "2838", "serial": "00000001",
	})
	writeDevice(t, root, "1-1.3", map[string]string{
		"idVendor": "0bda", "idProduct": "2832", "serial": "978",
	})

	got := List(root)
	if len(got) != 3 {
		t.Fatalf("List = %+v, want 3 devices", got)
	}
	wantBus := []string{"1-1.2", "1-1.3", "1-1.4"}
	for i, d := range got {
		if d.BusPath != wantBus[i] {
			t.Fatalf("order = %+v, want bus paths %v", got, wantBus)
		}
	}
	if !got[0].Duplicate || got[1].Duplicate || !got[2].Duplicate {
		t.Fatalf("duplicate flags wrong: %+v", got)
	}
}

func TestList_PreservesWhitespacePaddedSerialVerbatim(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDevice(t, root, "1-1.2", map[string]string{
		"idVendor": "0bda", "idProduct": "2838", "serial": " 1090 ",
	})
	got := List(root)
	if len(got) != 1 || got[0].Serial != " 1090 " {
		t.Fatalf("List = %+v, want serial preserved verbatim (\" 1090 \")", got)
	}
}

func TestList_NormalizesVidPidCase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDevice(t, root, "1-1.2", map[string]string{
		"idVendor": "0BDA", "idProduct": "2838", "serial": "1090",
	})
	if got := List(root); len(got) != 1 {
		t.Fatalf("List = %+v, want uppercase vid matched", got)
	}
}
