package bluetooth

import (
	"sync/atomic"

	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/entity"
	environmental "github.com/avanha/pmaas-spi/environment"
)

// ThermometerHandle is the assembly's handle on a thermometer named with AddThermometer. It is an
// environment.IWirelessThermometer, so it goes wherever one does (an automation's temperature condition, say), and can be asked from
// any goroutine. Before the plugin has started, and after it stops, it answers as a thermometer that has not reported anything.
type ThermometerHandle struct {
	name     string
	read     atomic.Pointer[func() (environmental.WirelessThermometer, error)]
	entityId atomic.Pointer[string]
}

var _ environmental.IWirelessThermometer = (*ThermometerHandle)(nil)

// ThermometerHandle is an entity.Ref, so that the assembly can put the thermometer in a group.
var _ entity.Ref = (*ThermometerHandle)(nil)

// RegisteredId is the id of the thermometer's entity, or false until the plugin has registered it (which it does when it starts) and
// after it has forgotten it.
func (h *ThermometerHandle) RegisteredId() (string, bool) {
	if id := h.entityId.Load(); id != nil && *id != "" {
		return *id, true
	}

	return "", false
}

func (h *ThermometerHandle) setEntityId(id string) {
	if id == "" {
		h.entityId.Store(nil)

		return
	}

	h.entityId.Store(&id)
}

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
