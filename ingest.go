package bluetooth

import (
	"fmt"
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/parser"
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

// onAdvertisement takes in an advertisement from a source. Runs on the plugin goroutine.
func (p *plugin) onAdvertisement(eventInfo *events.EventInfo) error {
	if advertisement, ok := eventInfo.Event.(bluetooth.AdvertisementEvent); ok {
		p.ingest(advertisement)
	}

	return nil
}

func (p *plugin) ingest(a bluetooth.AdvertisementEvent) {
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

	if dev.parser == nil {
		if chosen := parser.Choose(dev.known); chosen != nil {
			fmt.Printf("%T %s (%s) is a %s\n", p, dev.Name, dev.Address, chosen.MakeAndModel)

			dev.parser = chosen
			p.applyDeviceType(dev, chosen.Type, chosen.MakeAndModel)

			changed = true
		}
	}

	if dev.parser != nil && (len(a.ManufacturerData) > 0 || len(a.ServiceData) > 0) {
		if result, ok := dev.parser.Parse(a.Fields); ok && p.applyResult(dev, result, now) {
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

// applyResult puts what a parser found on the device, and reports whether anything about it changed.
func (p *plugin) applyResult(dev *device, result parser.Result, now time.Time) bool {
	changed := false

	if result.BatteryLevel != -1 {
		changed = dev.BatteryData.Level != result.BatteryLevel
		dev.BatteryData = environmental.BatteryData{Level: result.BatteryLevel, LastUpdateTime: now}
	}

	if !result.Environment.IsEmpty() {
		tempUpdated := dev.SensorData.Temperature != result.Environment.Temperature
		humidityUpdated := dev.SensorData.Humidity != result.Environment.Humidity

		dev.SensorData = environmental.SensorData{
			HasData:        true,
			Temperature:    result.Environment.Temperature,
			HasHumidity:    result.Environment.Humidity >= 0,
			Humidity:       result.Environment.Humidity,
			LastUpdateTime: now,
		}

		changed = changed || tempUpdated || humidityUpdated
	}

	return changed
}

func (p *plugin) applyDeviceType(dev *device, deviceType common.DeviceType, makeAndModel string) {
	dev.DeviceType = deviceType
	dev.DeviceMakeAndModel = makeAndModel

	if dev.PmaasEntityId == "" {
		p.registerDevice(dev)
	}
}

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

		// device.LastUpdateTime is always set when the device is created from an advertisement.
		if lruDevice == nil || device.LastUpdateTime.Before(lruDevice.LastUpdateTime) {
			lruDevice = device
		}
	}

	if lruDevice != nil {
		fmt.Printf("Trimming device %s (%s), last updated at %v from device map\n", lruDevice.Name, lruDevice.Address, lruDevice.LastUpdateTime)

		if lruDevice.PmaasEntityId != "" {
			if err := p.container.DeregisterEntity(lruDevice.PmaasEntityId); err != nil {
				fmt.Printf("Device %s (%s) could not be deregistered: %v\n", lruDevice.Name, lruDevice.Address, err)
			}
		}

		delete(p.devices, lruDevice.Address)
	}
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
