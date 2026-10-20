package bluetooth

import (
	"fmt"
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// PluginConfig configures the plugin. Where advertisements are heard is not its business: add a source plugin for each place
// (pmaas-plugin-bluez for the local radio; ESPHome Bluetooth proxies come with the esphome plugin).
type PluginConfig struct {
	// EnableTestDevices adds a thermometer that doesn't exist, to see how one is shown.
	EnableTestDevices bool

	// ListAllDevices is whether to keep a list of every named device the sources hear, wanted or not, and show it on the
	// page: it is how to find the address of one to configure. The default is true. A device that is not wanted costs a
	// sighting a minute while it is heard, and is forgotten when many more have been heard, or sooner if its address changes
	// (a phone's does about every quarter of an hour). Turn it off and only what the models recognise and what is configured
	// is listed.
	ListAllDevices bool

	// ActiveScan is whether to ask the sources to scan actively, which gets the scan responses some devices put their names in
	// (the Inkbird is known by its name, and a phone is only listed once it has one). The default is passive, which costs the radio
	// less and hears a beacon's brief burst more reliably, because an active scanner stops listening to ask each device it hears
	// for more. The Govee thermometers and a beacon button need nothing but passive.
	ActiveScan bool

	// ActiveScanEvery and ActiveScanFor are how often, and for how long, to scan actively when something configured needs scan
	// responses (an Inkbird, see AddInkbirdThermometer) and ActiveScan is not already on: the rest of the time the scan is
	// passive. They default to 3 minutes and 30 seconds. They are rounded up to whole minutes at the start (the plugin looks
	// once a minute), and a window shorter than the time a device takes to repeat itself can miss it.
	ActiveScanEvery time.Duration
	ActiveScanFor   time.Duration

	devices map[string]*device
	handles map[string]*ThermometerHandle
	models  []bluetooth.DeviceModel
	added   []bluetooth.ConfiguredDevice
}

func NewPluginConfig() PluginConfig {
	return PluginConfig{
		EnableTestDevices: false,
		ListAllDevices:    true,
		ActiveScanEvery:   3 * time.Minute,
		ActiveScanFor:     30 * time.Second,
		devices:           make(map[string]*device),
		handles:           make(map[string]*ThermometerHandle),
	}
}

// Recognize adds models of devices to recognise without their being configured: the sources are asked for what the models want,
// and a device that one of them identifies becomes an entity. The devices module (pmaas-bluetooth-devices) has them, and
// devices.All() is all of them. A model whose name has been added already is ignored, so overlapping lists are harmless.
func (c *PluginConfig) Recognize(models ...bluetooth.DeviceModel) {
	for _, model := range models {
		if model == nil {
			panic("bluetooth: Recognize was given a nil model")
		}

		known := false

		for _, existing := range c.models {
			known = known || existing.Name() == model.Name()
		}

		if !known {
			c.models = append(c.models, model)
		}
	}
}

// Add adds a device the assembly configures, which a device package's builder makes (inkbird.Thermometer, bluecharm.Button), or
// IBeaconButton. It panics if the device is badly configured, or its address is already named, like regexp.MustCompile does for a
// pattern that can't be: it is written in the program.
//
// For a thermometer with an address it returns the handle on it, as AddThermometer does; for any other device it returns nil.
func (c *PluginConfig) Add(device bluetooth.ConfiguredDevice) *ThermometerHandle {
	if device == nil {
		panic("bluetooth: Add was given a nil device")
	}

	if err := validateConfigured(device); err != nil {
		panic(fmt.Sprintf("bluetooth: %v", err))
	}

	if addressed, ok := device.(bluetooth.Addressed); ok {
		address := addressed.Address()

		if existing, ok := c.devices[address]; ok {
			panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", address, existing.LocalName))
		}

		for _, other := range c.added {
			if o, ok := other.(bluetooth.Addressed); ok && o.Address() == address {
				panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", address, other.Name()))
			}
		}
	}

	c.added = append(c.added, device)

	if addressed, ok := device.(bluetooth.Addressed); ok && device.Kind() == bluetooth.KindThermometer {
		handle := &ThermometerHandle{name: device.Name()}
		c.handles[addressed.Address()] = handle

		return handle
	}

	return nil
}

// validateConfigured is what is wrong with how a device was configured, if anything.
func validateConfigured(device bluetooth.ConfiguredDevice) error {
	if validator, ok := device.(bluetooth.Validator); ok {
		if err := validator.Validate(); err != nil {
			return err
		}
	}

	if _, ok := device.(bluetooth.Addressed); !ok && device.Kind() == bluetooth.KindButton {
		return fmt.Errorf("the button %q has no fixed address: a button has to have one", device.Name())
	}

	return nil
}

// AddThermometer names a thermometer by its address, so that it is a thermometer from the first advertisement and the
// bluetooth plugin asks the sources for it. What it is, is for the models to recognise (see Recognize); to say what it is, use
// a device package's builder with Add. The address is any case, with ":" or "-"; it panics if it is not an address or has been
// added already, like regexp.MustCompile does for a pattern that can't be: it is written in the program.
//
// It returns the handle on the thermometer, which an automation can read (see ThermometerHandle).
func (c *PluginConfig) AddThermometer(address string, name string) *ThermometerHandle {
	normal, ok := bluetooth.NormalizeAddress(address)
	if !ok {
		panic(fmt.Sprintf("Device address %q is not a Bluetooth address like AA:BB:CC:DD:EE:FF", address))
	}

	if existing, ok := c.devices[normal]; ok {
		panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", normal, existing.LocalName))
	}

	for _, other := range c.added {
		if o, ok := other.(bluetooth.Addressed); ok && o.Address() == normal {
			panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", normal, other.Name()))
		}
	}

	c.devices[normal] = &device{Address: normal, LocalName: name, DeviceType: common.Thermometer}
	handle := &ThermometerHandle{name: name}
	c.handles[normal] = handle

	return handle
}
