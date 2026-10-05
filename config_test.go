package bluetooth

import (
	"strings"
	"testing"
)

func TestAddThermometer_DuplicateAddressPanicsNamingTheExistingDevice(t *testing.T) {
	config := NewPluginConfig()
	config.AddThermometer("AA:BB:CC:DD:EE:FF", "Garage")

	defer func() {
		message, ok := recover().(string)

		if !ok {
			t.Fatal("expected a panic with a message")
		}

		if !strings.Contains(message, `already registered as "Garage"`) || !strings.Contains(message, "AA:BB:CC:DD:EE:FF") {
			t.Fatalf("unexpected message %q", message)
		}
	}()

	config.AddThermometer("AA:BB:CC:DD:EE:FF", "Attic")
}
