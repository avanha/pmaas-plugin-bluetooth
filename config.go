package bluetooth

import (
	"fmt"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
	spidevice "github.com/avanha/pmaas-spi/device"
)

// PluginConfig configures the plugin. Where advertisements are heard is not its business: add a source plugin for each place
// (pmaas-plugin-bluez for the local radio; ESPHome Bluetooth proxies come with the esphome plugin).
type PluginConfig struct {
	// EnableTestDevices adds a thermometer that doesn't exist, to see how one is shown.
	EnableTestDevices bool

	// ListAllDevices is whether to keep a list of every named device the sources hear, wanted or not, and show it on the
	// page: it is how to find the address of one to configure. The default is true. A device that is not wanted costs a
	// sighting a minute while it is heard, and is forgotten when many more have been heard, or sooner if its address changes
	// (a phone's does about every quarter of an hour). Turn it off and only what the parsers recognise and what is configured
	// is listed.
	ListAllDevices bool

	devices map[string]*device
	buttons []*BeaconButton
}

func NewPluginConfig() PluginConfig {
	return PluginConfig{
		EnableTestDevices: false,
		ListAllDevices:    true,
		devices:           make(map[string]*device),
	}
}

// AddBeaconButton adds a button that is a Bluetooth beacon (see BeaconButton), by the beacon's address, which is any case with
// ":" or "-". Say which frame is which press on what it returns; at least one is needed. It panics if the address is not a
// Bluetooth address or is already named, like regexp.MustCompile does for a pattern that can't be: it is written in the program.
func (c *PluginConfig) AddBeaconButton(address string, name string) *BeaconButton {
	normal, ok := bluetooth.NormalizeAddress(address)
	if !ok {
		panic(fmt.Sprintf("Button address %q is not a Bluetooth address like AA:BB:CC:DD:EE:FF", address))
	}

	if existing, ok := c.devices[normal]; ok {
		panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", normal, existing.LocalName))
	}

	for _, existing := range c.buttons {
		if existing.address == normal {
			panic(fmt.Sprintf("Device with address %s already registered as the button \"%s\"", normal, existing.name))
		}
	}

	button := &BeaconButton{address: normal, name: name, burst: defaultBurst, frames: make(map[spidevice.ButtonPress]bluetooth.IBeaconMatch)}
	c.buttons = append(c.buttons, button)

	return button
}

// AddThermometer names a thermometer by its address, so that it is a thermometer from the first advertisement and the
// bluetooth plugin asks the sources for it. The address is any case, with ":" or "-"; it panics if it is not an address or
// has been added already, like regexp.MustCompile does for a pattern that can't be: it is written in the program.
func (c *PluginConfig) AddThermometer(address string, name string) {
	normal, ok := bluetooth.NormalizeAddress(address)
	if !ok {
		panic(fmt.Sprintf("Device address %q is not a Bluetooth address like AA:BB:CC:DD:EE:FF", address))
	}

	existing, ok := c.devices[normal]
	if ok {
		panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", normal, existing.LocalName))
	}

	for _, button := range c.buttons {
		if button.address == normal {
			panic(fmt.Sprintf("Device with address %s already registered as the button \"%s\"", normal, button.name))
		}
	}

	c.devices[normal] = &device{
		Address:    normal,
		LocalName:  name,
		DeviceType: common.Thermometer,
	}
}
