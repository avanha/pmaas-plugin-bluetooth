package bluetooth

import (
	"sync/atomic"

	spi "github.com/avanha/pmaas-spi"
	environmental "github.com/avanha/pmaas-spi/environment"
)

// ThermometerHandle is the assembly's handle on a thermometer named with AddThermometer. It is an
// environment.IWirelessThermometer, so it goes wherever one does (an automation's temperature condition, say), and can be asked from
// any goroutine. Before the plugin has started, and after it stops, it answers as a thermometer that has not reported anything.
type ThermometerHandle struct {
	name string
	read atomic.Pointer[func() (environmental.WirelessThermometer, error)]
}

var _ environmental.IWirelessThermometer = (*ThermometerHandle)(nil)

// Name is the thermometer's name.
func (h *ThermometerHandle) Name() string {
	return h.name
}

func (h *ThermometerHandle) GetWirelessThermometerData() environmental.WirelessThermometer {
	if read := h.read.Load(); read != nil {
		if data, err := (*read)(); err == nil {
			return data
		}
	}

	return environmental.WirelessThermometer{Name: h.name}
}

// attach is for the plugin: reads go to its goroutine, which owns the device.
func (h *ThermometerHandle) attach(container spi.IPMAASContainer, read func() environmental.WirelessThermometer) {
	f := func() (environmental.WirelessThermometer, error) { return spi.Exec(container, read) }
	h.read.Store(&f)
}

func (h *ThermometerHandle) detach() {
	h.read.Store(nil)
}
