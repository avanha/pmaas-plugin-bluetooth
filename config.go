package bluetooth

import "fmt"
import "pmaas.io/plugins/bluetooth/common"

const DEFAULT_ADAPTER string = "DEFAULT_ADAPTER"

type BluetoothPluginConfig struct {
	Adapter           string
	EnableTestDevices bool
	devices           map[string]*device
}

func NewBluetoothPluginConfig() BluetoothPluginConfig {
	return BluetoothPluginConfig{
		Adapter:           DEFAULT_ADAPTER,
		EnableTestDevices: false,
		devices:           make(map[string]*device),
	}
}

func (c *BluetoothPluginConfig) AddThermometer(address string, name string) {
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
