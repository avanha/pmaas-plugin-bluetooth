package scanner

import (
	"testing"

	"github.com/muka/go-bluetooth/bluez/profile/adapter"
)

func TestDeviceActionName(t *testing.T) {
	for action, want := range map[adapter.DeviceActions]string{
		adapter.DeviceAdded:      "device added",
		adapter.DeviceRemoved:    "device removed",
		adapter.DeviceActions(9): "unknown device action (9)",
	} {
		if got := deviceActionName(action); got != want {
			t.Errorf("deviceActionName(%d) = %q, want %q", action, got, want)
		}
	}
}
