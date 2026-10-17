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

func TestAddThermometer_TheAddressIsNormalisedAndAMalformedOneIsRefused(t *testing.T) {
	config := NewPluginConfig()
	config.AddThermometer("a4-c1-38-cc-e2-f0", "Garage")

	if _, ok := config.devices["A4:C1:38:CC:E2:F0"]; !ok {
		t.Errorf("devices %v", config.devices)
	}

	// The same address another way is the same address.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("no panic for a duplicate in another form")
			}
		}()

		config.AddThermometer("A4:C1:38:CC:E2:F0", "Attic")
	}()

	for _, bad := range []string{"", "nonsense", "A4:C1:38:CC:E2"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("no panic for %q", bad)
				}
			}()

			config.AddThermometer(bad, "x")
		}()
	}
}
