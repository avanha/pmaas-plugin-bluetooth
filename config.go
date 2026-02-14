package bluetooth

import "fmt"
import "github.com/avanha/pmaas-plugin-bluetooth/common"

const DefaultAdapter string = "DEFAULT_ADAPTER"

type PluginConfig struct {
	Adapter           string
	EnableTestDevices bool
	devices           map[string]*device
}

func NewPluginConfig() PluginConfig {
	return PluginConfig{
		Adapter:           DefaultAdapter,
		EnableTestDevices: false,
		devices:           make(map[string]*device),
	}
}

func (c *PluginConfig) AddThermometer(address string, name string) {
	existing, ok := c.devices[address]
	if ok {
		panic(fmt.Sprintf("Device with address %s already registered as \"%s\"", address, &existing.LocalName))
	}

	c.devices[address] = &device{
		Address:    address,
		LocalName:  name,
		DeviceType: common.Thermometer,
	}
}
