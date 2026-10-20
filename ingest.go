package bluetooth

import (
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
)

// signalFresh is how recently a source must have heard a device for its signal to count in choosing the best one.
const signalFresh = time.Minute

// signalKept is how long a source's signal is kept without being heard again.
const signalKept = 10 * time.Minute

// maxDevices is how many devices are kept when they have no name of their own.
const maxDevices = 100

// temporaryAge is how long a device with an address that changes is kept without being heard. Such addresses are changed about
// every quarter of an hour, so a device not heard from in ten minutes has most likely gone by another.
const temporaryAge = 10 * time.Minute

// onAdvertisement takes in an advertisement from a source. Runs on the plugin goroutine.
func (p *plugin) onAdvertisement(eventInfo *events.EventInfo) error {
	if advertisement, ok := eventInfo.Event.(bluetooth.AdvertisementEvent); ok {
		p.ingest(advertisement)
	}

	return nil
}

// onSighting takes in a source's word that a device is around. It is an advertisement with nothing in it to decode: what the
// device is called, what it lists, and how loudly it was heard. Runs on the plugin goroutine.
func (p *plugin) onSighting(eventInfo *events.EventInfo) error {
	sighting, ok := eventInfo.Event.(bluetooth.SightingEvent)
	if !ok {
		return nil
	}

	p.ingest(bluetooth.AdvertisementEvent{
		Source:      sighting.Source,
		Address:     sighting.Address,
		AddressType: sighting.AddressType,
		RSSI:        sighting.RSSI,
		Time:        sighting.Time,
		Fields:      bluetooth.Fields{LocalName: sighting.LocalName, ServiceUUIDs: sighting.ServiceUUIDs},
	})

	return nil
}

func (p *plugin) ingest(a bluetooth.AdvertisementEvent) {
	if b, ok := p.buttons[a.Address]; ok {
		p.hearButton(b, a)
	}

	now := p.now()
	changed := false

	dev, ok := p.devices[a.Address]
	if !ok {
		// Avoid blowing up the device map with too many records. This can happen when running for long periods when
		// devices with random addresses are present.
		p.trimDevices()

		dev = &device{Id: a.Address, Address: a.Address, UUIDs: make([]string, 0)}
		p.devices[a.Address] = dev
	}

	if dev.DeviceDiscoveryTime.IsZero() {
		dev.DeviceDiscoveryTime = now
	}

	if dev.AddressType == "" && a.AddressType != bluetooth.AddressUnknown {
		dev.AddressType = a.AddressType.String()
		dev.temporary = bluetooth.IsTemporaryAddress(a.Address, a.AddressType)
	}

	dev.LastUpdateTime = now
	dev.LastEventType = "advertisement"

	if a.LocalName != "" {
		dev.ReportedName = a.LocalName
		dev.known.LocalName = a.LocalName
	}

	if dev.LocalName == "" {
		dev.Name = dev.ReportedName
	} else {
		dev.Name = dev.LocalName
	}

	addUUIDs(dev, a.ServiceUUIDs)
	dev.known.ServiceUUIDs = dev.UUIDs

	if p.hearSignal(dev, a, now) {
		changed = true
	}

	if !dev.claimChecked {
		dev.claimChecked = true

		for _, claimer := range p.claimers {
			if p.claims(claimer, &a) {
				dev.LocalName = claimer.Name()
				dev.Name = claimer.Name()

				p.log.Debug("device claimed", "name", dev.Name, "address", dev.Address)
				p.adopt(dev, claimer)

				changed = true

				break
			}
		}
	}

	if dev.impl == nil {
		for _, model := range p.models {
			if model.Identify(dev.known) {
				p.log.Debug("device identified", "name", dev.Name, "address", dev.Address, "makeAndModel", model.Name())

				dev.model, dev.impl = model, model.New()
				p.applyKind(dev, model.Kind(), model.Name())

				changed = true

				break
			}
		}
	}

	if dev.impl != nil && (len(a.ManufacturerData) > 0 || len(a.ServiceData) > 0) {
		if result := p.hear(dev.Name, dev.impl, &dev.disabled, &a, now); p.applyResult(dev, result, now) {
			changed = true
		}
	}

	// A device that is typed is announced when something about it changes. (Becoming typed is a change.)
	if dev.DeviceType != common.Unknown && changed {
		p.broadcastStateChangedEvent(dev)
	}
}

// hearSignal notes how the source heard the device, and sets the device's signal to the strongest of the sources that have
// heard it lately. It reports whether the signal's strength changed.
func (p *plugin) hearSignal(dev *device, a bluetooth.AdvertisementEvent, now time.Time) bool {
	if dev.signals == nil {
		dev.signals = make(map[string]signal)
	}

	dev.signals[a.Source] = signal{rssi: a.RSSI, at: now}

	var best signal
	found := false

	for source, s := range dev.signals {
		age := now.Sub(s.at)

		if age > signalKept {
			delete(dev.signals, source)

			continue
		}

		if age <= signalFresh && (!found || s.rssi > best.rssi) {
			best, found = s, true
		}
	}

	previous := dev.RSSIData.RSSI
	dev.RSSIData = environmental.RSSIData{RSSI: best.rssi, LastUpdateTime: best.at}

	return previous != best.rssi
}

// adopt makes the device one the assembly configured. The entity is registered by the caller, once the plugin has started
// (see applyKind).
func (p *plugin) adopt(dev *device, configured bluetooth.ConfiguredDevice) {
	makeAndModel := configured.Name()
	if m, ok := configured.(bluetooth.MakeAndModeler); ok {
		makeAndModel = m.MakeAndModel()
	}

	dev.impl = configured.New()
	dev.DeviceType = deviceTypeOf(configured.Kind())
	dev.DeviceMakeAndModel = makeAndModel

	if dev.PmaasEntityId == "" && p.container != nil {
		p.registerDevice(dev)
	} else if p.container != nil {
		p.giveView(dev)
	}
}

// claims asks a configured device whether the advertisement is from it. It is someone else's code, so a panic is a no.
func (p *plugin) claims(configured bluetooth.ConfiguredDevice, a *bluetooth.AdvertisementEvent) (claimed bool) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("a device panicked deciding whether an advertisement is its own", "device", configured.Name(), "address", a.Address, "panic", r)

			claimed = false
		}
	}()

	return configured.Claims(a)
}

// applyResult puts what a device found on it, and reports whether anything about it changed.
func (p *plugin) applyResult(dev *device, result bluetooth.Result, now time.Time) bool {
	changed := false

	if result.Battery != nil {
		changed = dev.BatteryData.Level != *result.Battery
		dev.BatteryData = environmental.BatteryData{Level: *result.Battery, LastUpdateTime: now}
	}

	if result.Reading != nil {
		reading := *result.Reading
		tempUpdated := dev.SensorData.Temperature != reading.Temperature
		humidityUpdated := dev.SensorData.Humidity != reading.Humidity

		dev.SensorData = environmental.SensorData{
			HasData:        true,
			Temperature:    reading.Temperature,
			HasHumidity:    reading.HasHumidity,
			Humidity:       reading.Humidity,
			LastUpdateTime: now,
		}

		changed = changed || tempUpdated || humidityUpdated
	}

	return changed
}

// deviceTypeOf is the type of device that is of the kind.
func deviceTypeOf(kind bluetooth.Kind) common.DeviceType {
	if kind == bluetooth.KindThermometer {
		return common.ThermometerAndHygrometer
	}

	return common.Unknown
}

// applyKind makes the device of the kind, and registers its entity if it has none yet.
func (p *plugin) applyKind(dev *device, kind bluetooth.Kind, makeAndModel string) {
	dev.DeviceType = deviceTypeOf(kind)
	dev.DeviceMakeAndModel = makeAndModel

	if dev.PmaasEntityId == "" {
		p.registerDevice(dev)
	} else {
		p.giveView(dev)
	}
}

// trimDevices makes room for a device, if the map is full, by letting go of the one heard from least recently. One whose
// address changes goes first: it is the one that is not coming back.
func (p *plugin) trimDevices() {
	if len(p.devices) < maxDevices {
		return
	}

	var lruDevice *device

	for _, device := range p.devices {
		if device.LocalName != "" {
			// The device has a LocalName which means it was pre-registered and is not eligible for trimming.
			continue
		}

		if lruDevice == nil || trimsBefore(device, lruDevice) {
			lruDevice = device
		}
	}

	if lruDevice != nil {
		p.forget(lruDevice)
	}
}

// trimsBefore is whether the device is to be let go of before the other: a temporary address before one that is not, and
// otherwise the one heard from longer ago. LastUpdateTime is always set when a device is created from an advertisement.
func trimsBefore(device *device, other *device) bool {
	if device.temporary != other.temporary {
		return device.temporary
	}

	return device.LastUpdateTime.Before(other.LastUpdateTime)
}

// ageDevices lets go of the devices with an address that changes that have not been heard from for a while. Runs each minute,
// on the plugin goroutine. A device that was named in the configuration is never let go.
func (p *plugin) ageDevices(now time.Time) {
	for _, device := range p.devices {
		if device.temporary && device.LocalName == "" && now.Sub(device.LastUpdateTime) >= temporaryAge {
			p.forget(device)
		}
	}
}

// forget lets go of a device, and the entity that was registered for it.
func (p *plugin) forget(dev *device) {
	p.log.Debug("forgetting device", "name", dev.Name, "address", dev.Address, "lastUpdate", dev.LastUpdateTime)

	if dev.PmaasEntityId != "" {
		if err := p.container.DeregisterEntity(dev.PmaasEntityId); err != nil {
			p.log.Error("device could not be deregistered", "name", dev.Name, "address", dev.Address, "error", err)
		}
	}

	if handle := p.handles[dev.Address]; handle != nil {
		handle.setEntityId("")
	}

	delete(p.devices, dev.Address)
}

func addUUIDs(dev *device, uuids []string) {
	for _, uuid := range uuids {
		if indexOf(uuid, dev.UUIDs) != -1 {
			continue
		}

		dev.UUIDs = append(dev.UUIDs, uuid)
	}
}

func indexOf(e string, values []string) int {
	for i, value := range values {
		if value == e {
			return i
		}
	}

	return -1
}
