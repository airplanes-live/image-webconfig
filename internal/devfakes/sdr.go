package devfakes

import (
	"os"
	"path/filepath"
)

// SeedSDRSysfs lays a fake /sys/bus/usb/devices tree under stateDir and
// returns its root, for server.Deps.SDRSysfsRoot. The fixture deliberately
// exercises every UI warning path: one cleanly-pinnable stick ("1090") and
// two sticks sharing the factory-default serial "00000001" — which is both
// a duplicate and numeric-index-ambiguous (readsb's device search parses it
// as index 1).
func SeedSDRSysfs(stateDir string) string {
	root := filepath.Join(stateDir, "sysfs-usb")
	write := func(busPath, serial string) {
		dir := filepath.Join(root, busPath)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "idVendor"), []byte("0bda\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "idProduct"), []byte("2838\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "serial"), []byte(serial+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "product"), []byte("RTL2838UHIDIR\n"), 0o644)
	}
	write("1-1.2", "1090")
	write("1-1.3", "00000001")
	write("1-1.4", "00000001")
	return root
}
