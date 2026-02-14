package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/common"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/parser/govee"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/parser/inkbird"
	dbus "github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
)

type Scanner struct {
	devices            map[dbus.ObjectPath]*common.ObservedDevice
	watchConsumerCount int
}

const EVENT_TYPE_DEVICE_DISCOVERED string = "DEVICE_DISCOVERED"
const EVENT_TYPE_ENVIRONMENT_DATA_UPDATED string = "ENVIRONMENT_DATA_UPDATED"
const EVENT_TYPE_MANUFACTURER_DATA_UPDATED string = "EVENT_TYPE_MANUFACTURER_DATA_UPDATED"
const EVENT_TYPE_SERVICE_DATA_UPDATED string = "EVENT_TYPE_SERVICE_DATA_UPDATED"
const EVENT_TYPE_RSSI_UPDATED string = "EVENT_TYPE_RSSI_UPDATED"
const EVENT_TYPE_UUIDS_UPDATED string = "EVENT_TYPE_UUIDS_UPDATED"
const EVENT_TYPE_BATTERY_LEVEL_UPDATED string = "EVENT_TYPE_BATTERY_LEVEL_UPDATED"
const EVENT_TYPE_DEVICE_TYPE_UPDATED string = "DEVICE_TYPE_UPDATED"

type ScanEvent struct {
	EventTime          time.Time
	Type               string
	DeviceId           string
	DeviceType         pc.DeviceType
	DeviceMakeAndModel string
	Address            string
	AddressType        string
	Name               string
	RSSI               int
	UUIDs              []string
	ManufacturerData   map[uint16]interface{}
	ServiceData        map[string]interface{}
	BatteryLevel       int `default:"-1"`
	EnvironmentData    pc.EnvironmentData
}

type StartScanResult struct {
	scanEventCh chan *ScanEvent
	cancelFunc  func()
}

type devicePropertyChanged struct {
	bluezEvent *bluez.PropertyChanged
	device     *common.ObservedDevice
	eventTime  time.Time
	newValue   interface{}
}

func NewScanner() *Scanner {
	return &Scanner{
		devices:            make(map[dbus.ObjectPath]*common.ObservedDevice),
		watchConsumerCount: 0,
	}
}

func (p *Scanner) StartScan(adapterId string) (chan *ScanEvent, func(), error) {
	var err error = nil
	var adapter1 *adapter.Adapter1

	adapter1, err = api.GetAdapter(adapterId)

	if err != nil {
		return nil, nil, fmt.Errorf("error starting scan, unable to get adapter \"%s\": %w", adapterId, err)
	}

	fmt.Printf("Retrieved adapter interface %v\n", adapter1.Path())

	//fmt.Println("Flushing cached devices...")
	//err = adapter1.FlushDevices()
	//
	//if err != nil {
	//	return nil, nil, fmt.Errorf("error starting scan, unable to flush cached devices: %w", err)
	//}

	var filter *adapter.DiscoveryFilter = nil

	discoveryCh, discoverCancelFunc, err := api.Discover(adapter1, filter)

	if err != nil {
		return nil, nil, fmt.Errorf("error starting scan, unable to start discovery: %w", err)
	}

	scanEventCh := make(chan *ScanEvent, 10)
	scanDone := make(chan error)

	go p.readDiscoveryEvents(discoveryCh, scanEventCh, scanDone)

	cancelFunc := func() {
		discoverCancelFunc()
		err := <-scanDone

		if err == nil {
			fmt.Println("Discovery stopped")
		} else {
			fmt.Printf("Discovery stopped with error: %s", err)
		}

		// Don't exit since we can't make any more calls to Bluez
		// afterwards.
		//api.Exit()
	}

	return scanEventCh, cancelFunc, nil
}

func (p *Scanner) readDiscoveryEvents(
	discoveryCh chan *adapter.DeviceDiscovered,
	scanEventCh chan *ScanEvent,
	scanDoneCh chan error) {
	fmt.Println("Start readDiscoveryEvents")
	propChangeCh := make(chan *devicePropertyChanged, 100)
	propChangeConsumerDoneCh := make(chan *common.ObservedDevice)
	ctx := context.Background()
LOOP:
	for {
		select {
		case discoveryEv := <-discoveryCh:
			if discoveryEv == nil {
				fmt.Println("Received nil discovery event")
				break LOOP
			}
			eventTime := time.Now()
			p.handleDiscoveryEvent(ctx, discoveryEv, eventTime, propChangeCh, scanEventCh, propChangeConsumerDoneCh)
			break
		case propChangeEv := <-propChangeCh:
			p.handlePropChangeEvent(propChangeEv, scanEventCh)
			break
		case observedDevice := <-propChangeConsumerDoneCh:
			p.watchConsumerCount = p.watchConsumerCount - 1
			fmt.Printf("PropChange consumer for %s is complete, %d active consumers\n", observedDevice.ObjectPath, p.watchConsumerCount)
			break
		}
	}

	// At this point, there will be no more discovery events,
	// unwatch any currently present devices
	for path, dev := range p.devices {
		dev.Device.UnwatchProperties(dev.PropertyChangeCh)
		delete(p.devices, path)
	}

	if p.watchConsumerCount == 0 {
		// There are no active property watch consumers, so close the channels now
		close(propChangeCh)
		close(propChangeConsumerDoneCh)
	} else {
		// There is at least one active property watch consumer running, so continue consuming
		// property change events, until all consumers are stopped.
	REMLOOP:
		for {
			select {
			case propChangeEv := <-propChangeCh:
				p.handlePropChangeEvent(propChangeEv, scanEventCh)
				break
			case observedDevice := <-propChangeConsumerDoneCh:
				p.watchConsumerCount = p.watchConsumerCount - 1
				fmt.Printf("PropChange consumer for %s is complete, %d active consumers\n", observedDevice.ObjectPath, p.watchConsumerCount)

				if p.watchConsumerCount == 0 {
					break REMLOOP
				}
				break
			}
		}

		// Close both channels since there are no more running watch property consumers
		close(propChangeCh)
		close(propChangeConsumerDoneCh)

		// Consume the remainder of property change events that were in the propChangeCh buffer
		for propChangeEv := range propChangeCh {
			p.handlePropChangeEvent(propChangeEv, scanEventCh)
		}
	}

	close(scanEventCh)
	close(scanDoneCh)
	fmt.Println("End readDiscoveryEvents")
}

func (p *Scanner) handleDiscoveryEvent(
	_ context.Context,
	ev *adapter.DeviceDiscovered,
	eventTime time.Time,
	propChangeCh chan *devicePropertyChanged,
	scanEventCh chan *ScanEvent,
	propChangeConsumerDoneCh chan *common.ObservedDevice) {
	if ev.Type == adapter.DeviceRemoved {
		fmt.Printf("Received DeviceRemoved event, path: %s\n", ev.Path)
		dev, ok := p.devices[ev.Path]

		if ok {
			fmt.Printf("Device record found, calling UnwatchProperties, path: %s\n", ev.Path)
			dev.Device.UnwatchProperties(dev.PropertyChangeCh)
			delete(p.devices, ev.Path)
		} else {
			fmt.Printf("Device record not found, path: %s\n", ev.Path)
		}
	} else if ev.Type == adapter.DeviceAdded {
		fmt.Printf("Received DeviceAdded event, path: %s\n", ev.Path)

		existingDev, ok := p.devices[ev.Path]

		if ok {
			fmt.Printf("Existing device record found, calling UnwatchProperties, path: %s\n", ev.Path)
			existingDev.Device.UnwatchProperties(existingDev.PropertyChangeCh)
			delete(p.devices, ev.Path)
		}

		dev, err := device.NewDevice1(ev.Path)
		if err == nil {
			fmt.Printf("New device, watching for changes, name: \"%s\", address: %s, rssi: %d, addressType: %s, UUIDs: %v, manufacturerData length: %d, manufacturerData: %v\n",
				dev.Properties.Name, dev.Properties.Address, dev.Properties.RSSI, dev.Properties.AddressType,
				dev.Properties.UUIDs, len(dev.Properties.ManufacturerData), dev.Properties.ManufacturerData)
			observedDevice := common.ObservedDevice{
				ObjectPath:       ev.Path,
				Address:          dev.Properties.Address,
				AddressType:      dev.Properties.AddressType,
				PropertyChangeCh: nil,
				UUIDs:            dev.Properties.UUIDs,
				ParseFunc:        nil,
				Device:           dev,
				Type:             pc.Unknown,
				MakeAndModel:     "",
			}
			p.chooseParser(&observedDevice)
			var parseOk bool = false
			var parseResult common.ParseResult

			if observedDevice.ParseFunc != nil {
				parseOk, parseResult = observedDevice.ParseFunc(
					&observedDevice,
					/* changePropertyName = */ "",
					/* newPropertyValue = */ nil)
			}

			p.devices[ev.Path] = &observedDevice
			p.watchDevice(&observedDevice, propChangeCh, propChangeConsumerDoneCh)

			// Publish a scan event
			scanEvent := &ScanEvent{
				EventTime:          eventTime,
				Type:               EVENT_TYPE_DEVICE_DISCOVERED,
				DeviceId:           string(observedDevice.ObjectPath),
				DeviceType:         observedDevice.Type,
				DeviceMakeAndModel: observedDevice.MakeAndModel,
				Name:               observedDevice.Device.Properties.Name,
				Address:            observedDevice.Address,
				AddressType:        observedDevice.AddressType,
				RSSI:               int(observedDevice.Device.Properties.RSSI),
				UUIDs:              observedDevice.Device.Properties.UUIDs,
				ManufacturerData:   observedDevice.Device.Properties.ManufacturerData,
				ServiceData:        observedDevice.Device.Properties.ServiceData,
			}

			if parseOk {
				scanEvent.BatteryLevel = parseResult.BatteryLevel
				scanEvent.EnvironmentData = parseResult.EnvironmentData
			}
			scanEventCh <- scanEvent
		} else {
			fmt.Printf("Failed to get device interface for path %s: %s\n", ev.Path, err)
		}
	} else {
		fmt.Printf("Received %s event, path: %s\n", ev.Type, ev.Path)
	}
}

func (p *Scanner) watchDevice(dev *common.ObservedDevice, propChangeCh chan *devicePropertyChanged, propChangeConsumerDoneCh chan *common.ObservedDevice) {
	ch, err := dev.Device.WatchProperties()

	if err != nil {
		fmt.Printf("Failed to watch properties of %s (%s): %s\n", dev.Device.Properties.Name, dev.Device.Properties.Address, err)
		return
	}

	// Save the property change channel, so we can call device.UnwatchProperties when the device is removed,
	// or it's time shutdown.
	dev.PropertyChangeCh = ch
	p.watchConsumerCount = p.watchConsumerCount + 1

	go func() {
		fmt.Printf("Consuming property change events for %s (%s)\n", dev.Device.Properties.Name, dev.Device.Properties.Address)
		for evt := range ch {
			if evt == nil {
				fmt.Printf("Received nil property change event for %s (%s)\n", dev.Device.Properties.Name, dev.Device.Properties.Address)
				break
			}
			eventTime := time.Now()
			fmt.Printf("Received property change event %s for %s (%s)\n", evt.Name, dev.Device.Properties.Name, dev.Device.Properties.Address)

			// Capture the new value now, in this go routine, before we send it on the propChangeCh
			// which is consumed by a different go routine.
			var newValue interface{} = nil

			if evt.Name == "ManufacturerData" {
				newValue = dev.Device.Properties.ManufacturerData
			} else if evt.Name == "RSSI" {
				newValue = dev.Device.Properties.RSSI
			} else if evt.Name == "UUIDs" {
				newValue = dev.Device.Properties.UUIDs
			} else if evt.Name == "ServiceData" {
				newValue = dev.Device.Properties.ServiceData
			}

			propChangeCh <- &devicePropertyChanged{
				bluezEvent: evt,
				device:     dev,
				eventTime:  eventTime,
				newValue:   newValue,
			}
		}
		fmt.Printf("Stopped consumption of property change events for %s (%s)\n", dev.Device.Properties.Name, dev.Device.Properties.Address)
		propChangeConsumerDoneCh <- dev
	}()
}

func (p *Scanner) handlePropChangeEvent(evt *devicePropertyChanged, scanEventCh chan *ScanEvent) {
	if evt.bluezEvent.Name == "ManufacturerData" {
		fmt.Printf("%s (%s) new ManufacturerData: %v\n", evt.device.Device.Properties.Name, evt.device.Device.Properties.Address, evt.newValue)
		currentDeviceType := evt.device.Type

		// If there isn't a parse function already, try choosing a parse function again now that we have more data
		if evt.device.ParseFunc == nil {
			p.chooseParser(evt.device)
		}

		if evt.device.Type != currentDeviceType {
			scanEvent := newScanEvent(EVENT_TYPE_DEVICE_TYPE_UPDATED, evt)
			scanEventCh <- scanEvent
		}

		if evt.device.ParseFunc != nil {
			ok, parseResult := evt.device.ParseFunc(evt.device, evt.bluezEvent.Name, evt.newValue)
			if ok {
				if parseResult.BatteryLevel != -1 {
					scanEvent := newScanEvent(EVENT_TYPE_BATTERY_LEVEL_UPDATED, evt)
					scanEvent.BatteryLevel = parseResult.BatteryLevel
					scanEventCh <- scanEvent
				}

				if !parseResult.EnvironmentData.IsEmpty() {
					scanEvent := newScanEvent(EVENT_TYPE_ENVIRONMENT_DATA_UPDATED, evt)
					scanEvent.EnvironmentData = parseResult.EnvironmentData
					scanEventCh <- scanEvent
				}
			} else {
				scanEvent := newScanEvent(EVENT_TYPE_MANUFACTURER_DATA_UPDATED, evt)
				scanEvent.ManufacturerData = evt.newValue.(map[uint16]interface{})
				scanEventCh <- scanEvent
			}
		}
	} else if evt.bluezEvent.Name == "RSSI" {
		fmt.Printf("%s (%s) new RSSI: %v\n", evt.device.Device.Properties.Name, evt.device.Address, evt.newValue)
		scanEvent := newScanEvent(EVENT_TYPE_RSSI_UPDATED, evt)
		scanEvent.RSSI = int(evt.newValue.(int16))
		scanEventCh <- scanEvent
	} else if evt.bluezEvent.Name == "ServiceData" {
		fmt.Printf("%s (%s) new ServiceData: %v\n", evt.device.Device.Properties.Name, evt.device.Address, evt.newValue)
		scanEvent := newScanEvent(EVENT_TYPE_SERVICE_DATA_UPDATED, evt)
		scanEvent.ServiceData = evt.newValue.(map[string]interface{})
		scanEventCh <- scanEvent
	} else if evt.bluezEvent.Name == "UUIDs" {
		fmt.Printf("%s (%s) new UUIDs: %v\n", evt.device.Device.Properties.Name, evt.device.Address, evt.newValue)
		scanEvent := newScanEvent(EVENT_TYPE_UUIDS_UPDATED, evt)
		scanEvent.UUIDs = evt.newValue.([]string)
		scanEventCh <- scanEvent
	}
}

func newScanEvent(eventType string, evt *devicePropertyChanged) *ScanEvent {
	return &ScanEvent{
		Type:               eventType,
		EventTime:          evt.eventTime,
		DeviceId:           string(evt.device.ObjectPath),
		DeviceType:         evt.device.Type,
		DeviceMakeAndModel: evt.device.MakeAndModel,
		Address:            evt.device.Address,
	}
}

func (p *Scanner) chooseParser(dev *common.ObservedDevice) {
	for _, uuid := range dev.UUIDs {
		if strings.HasPrefix(uuid, "0000ec88") {
			dev.MakeAndModel = "Govee GVH5075"
			dev.Type = pc.ThermometerAndHygrometer
			dev.ParseFunc = govee.Parse
			break
		} else if uuid == "0000fff0-0000-1000-8000-00805f9b34fb" && dev.Device.Properties.Name == "sps" {
			fmt.Println("Chose inkbird parser")
			dev.MakeAndModel = "Inkbird IBS-TH"
			dev.Type = pc.ThermometerAndHygrometer
			dev.ParseFunc = inkbird.Parse
			break
		}
	}
}
