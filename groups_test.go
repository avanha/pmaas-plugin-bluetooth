package bluetooth

import (
	"github.com/avanha/pmaas-bluetooth-devices/bluecharm"
	"github.com/avanha/pmaas-bluetooth-devices/inkbird"
	"testing"

	spibluetooth "github.com/avanha/pmaas-spi/bluetooth"
	spidevice "github.com/avanha/pmaas-spi/device"
	"github.com/avanha/pmaas-spi/group"
)

func TestGroups_AThermometerHandleNamesItsEntityAndItsViewIsItsCard(t *testing.T) {
	config := NewPluginConfig()
	handle := config.AddThermometer(goveeAddress, "Master Bedroom")

	if _, ok := handle.RegisteredId(); ok {
		t.Fatal("the handle has an id before the plugin has started")
	}

	f := newFixture(t, config)

	// Heard, so it is known to be a thermometer, which is when it is given a view.
	f.hear("s", goveeAddress, -61, goveeFrame)

	id, ok := handle.RegisteredId()
	if !ok || id == "" {
		t.Fatalf("%q %v", id, ok)
	}

	view, err := f.container.views[id]()
	if err != nil {
		t.Fatal(err)
	}

	card, isCard := view.Value.(publishedWirelessThermometer)
	if !isCard || card.WirelessThermometer.Name != "Master Bedroom" || card.SensorData.Temperature != 21.1 {
		t.Errorf("%#v", view.Value)
	}

	if view.Key != "bluetooth/"+goveeAddress || view.Category != group.CategoryThermometers {
		t.Errorf("key %q category %q", view.Key, view.Category)
	}

	f.plugin.Stop()

	if _, ok := handle.RegisteredId(); ok {
		t.Error("the handle still has an id after the plugin stopped")
	}
}

func TestGroups_AConfiguredThermometerHasAViewBeforeItIsHeardAndShowsNothingYet(t *testing.T) {
	config := NewPluginConfig()
	handle := config.AddThermometer(goveeAddress, "Master Bedroom")

	f := newFixture(t, config)

	id, ok := handle.RegisteredId()
	if !ok {
		t.Fatal("no entity yet")
	}

	view := f.container.views[id]
	if view == nil {
		t.Fatal("a thermometer that has not been heard has no view, so a page of groups would show it as an unexplained summary")
	}

	made, err := view()
	if err != nil {
		t.Fatal(err)
	}

	if card, isCard := made.Value.(publishedWirelessThermometer); !isCard || card.WirelessThermometer.Name != "Master Bedroom" || card.SensorData.HasData {
		t.Errorf("%#v", made.Value)
	}
}

func TestGroups_AButtonHandleNamesItsEntityAndItsViewIsItsCard(t *testing.T) {
	config, button := buttonConfig()

	if _, ok := button.RegisteredId(); ok {
		t.Fatal("the button has an id before the plugin has started")
	}

	f := newFixture(t, config)

	id, ok := button.RegisteredId()
	if !ok || id != "bluetooth_1" {
		t.Fatalf("%q %v", id, ok)
	}

	view, err := f.container.views[id]()
	if err != nil {
		t.Fatal(err)
	}

	if _, isCard := view.Value.(publishedBeacon); !isCard || view.Category != group.CategoryButtons || view.Key != "bluetooth/"+buttonAddress {
		t.Errorf("%#v", view)
	}

	if f.container.entities[buttonAddress] != spidevice.IButtonType {
		t.Error("the button is not registered as a button")
	}
}

func TestGroups_AThermometerAddedFromAModelHasAHandleToo(t *testing.T) {
	config := NewPluginConfig()
	handle := config.Add(inkbird.Thermometer(inkbirdAddres, "Patio"))

	if handle == nil {
		t.Fatal("no handle for the Inkbird thermometer")
	}

	f := newFixture(t, config)

	id, ok := handle.RegisteredId()
	if !ok || id == "" {
		t.Fatalf("%q %v", id, ok)
	}

	if f.container.views[id] == nil {
		t.Error("the Inkbird thermometer has no view")
	}

	if config.Add(bluecharm.Button("DD:88:00:00:3D:36", "Couch button").Single(spibluetooth.MatchAnyIBeacon().WithMajor(0))) != nil {
		t.Error("a button was given a thermometer handle")
	}
}
