package bluetooth

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"text/template"
	"time"

	"github.com/avanha/pmaas-spi/events"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner"
	"github.com/avanha/pmaas-spi"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/muka/go-bluetooth/api"
)

//go:embed content/static content/templates
var contentFS embed.FS

var bluetoothDeviceTemplate = spi.TemplateInfo{
	Name: "bluetooth_device",
	FuncMap: template.FuncMap{
		"RelativeTime": RelativeTime,
	},
	Paths:  []string{"templates/bluetooth_device.htmlt"},
	Styles: []string{"css/bluetooth_device.css"},
}

var IWirelessThermometerType = reflect.TypeOf((*environmental.IWirelessThermometer)(nil)).Elem()

// EnhancedSensorData Extend the spi/environment/SensorData with a locally-calculated Farenheit value
type EnhancedSensorData struct {
	environmental.SensorData
	TemperatureF float32
}

type RSSIData struct {
	RSSI           int
	LastUpdateTime time.Time
}

type Sortable interface {
	SortKey() string
}

type device struct {
	environmental.WirelessThermometer
	Id                  string
	DeviceType          common.DeviceType
	DeviceMakeAndModel  string
	PmaasEntityId       string
	Address             string
	AddressType         string
	ReportedName        string
	LocalName           string
	UUIDs               []string
	DeviceDiscoveryTime time.Time
	LastUpdateTime      time.Time
	LastEventType       string
}

func (d device) GetWirelessThermometerData() environmental.WirelessThermometer {
	return d.WirelessThermometer
}

type publishedDevice struct {
	dev            device
	Address        string
	Name           string
	RSSIData       environmental.RSSIData
	LastUpdateTime time.Time
}

func (d publishedDevice) SortKey() string {
	return d.Name
}

var publishedDeviceType = reflect.TypeOf((*publishedDevice)(nil)).Elem()

type publishedWirelessThermometer struct {
	environmental.WirelessThermometer
}

// Force implementation of Sortable
var _ Sortable = (*publishedWirelessThermometer)(nil)

func (d publishedWirelessThermometer) SortKey() string {
	return d.WirelessThermometer.Name
}

// Force implementation of spi.environment.IWirelessThermometer
var _ environmental.IWirelessThermometer = (*publishedWirelessThermometer)(nil)

func (d publishedWirelessThermometer) GetWirelessThermometerData() environmental.WirelessThermometer {
	return d.WirelessThermometer
}

type state struct {
	adapterId      string
	container      spi.IPMAASContainer
	scanCancelFunc func()
	devices        map[string]*device
}

type plugin struct {
	state *state
}

type BluetoothPlugin interface {
	spi.IPMAASPlugin
}

func NewPlugin(config PluginConfig) BluetoothPlugin {
	instance := &plugin{
		state: &state{
			adapterId:      "",
			container:      nil,
			scanCancelFunc: nil,
			devices:        make(map[string]*device),
		},
	}

	if config.Adapter == DefaultAdapter {
		instance.state.adapterId = api.GetDefaultAdapterID()
	} else {
		instance.state.adapterId = config.Adapter
	}

	for address, dev := range config.devices {
		instance.state.devices[address] = &device{
			Address:    dev.Address,
			LocalName:  dev.LocalName,
			DeviceType: dev.DeviceType,
			WirelessThermometer: environmental.WirelessThermometer{
				Name: dev.LocalName,
			},
		}
	}

	/*
		// A non-thermometer device to test rendering
		instance.state.devices["AA:BB:CC:DD:EE:FF"] = &device{
			Address:    "AA:BB:CC:DD:EE:FF",
			LocalName:  "Test Device",
			DeviceType: common.Unknown,
			WirelessThermometer: environmental.WirelessThermometer{
				RSSIData: environmental.RSSIData{
					RSSI:           -75,
					LastUpdateTime: time.Now(),
				},
			},
			LastUpdateTime: time.Now(),
		}
	*/

	if config.EnableTestDevices {
		testDeviceAddress := "AA:BB:CC:DD:EE:FF"
		now := time.Now()
		instance.state.devices[testDeviceAddress] = &device{
			Address:   "AA:BB:CC:DD:EE:FF",
			LocalName: "Test Thermometer",

			DeviceType: common.ThermometerAndHygrometer,
			WirelessThermometer: environmental.WirelessThermometer{
				Name: "Test Thermometer",
				RSSIData: environmental.RSSIData{
					RSSI:           -64,
					LastUpdateTime: now,
				},
				BatteryData: environmental.BatteryData{
					Level:          75,
					LastUpdateTime: now,
				},
				SensorData: environmental.SensorData{
					Temperature:    25.73,
					HasHumidity:    true,
					Humidity:       45.25,
					LastUpdateTime: now,
				},
			},
			LastUpdateTime: now,
		}
	}

	return instance
}

// Implementation of spi.IPMAASPlugin
var _ spi.IPMAASPlugin = (*plugin)(nil)

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.state.container = container
	container.ProvideContentFS(&contentFS, "content")
	container.EnableStaticContent("static")
	container.AddRoute("/plugins/bluetooth/", p.HandleHttpList)
}

var renderListOptions = spi.RenderListOptions{Title: "Bluetooth Devices"}

func (p *plugin) HandleHttpList(w http.ResponseWriter, r *http.Request) {
	publishedDevices := make([]Sortable, 0)

	for _, device := range p.state.devices {
		if device.ReportedName == "" && device.LocalName == "" {
			continue
		}

		var pd Sortable

		if device.DeviceType == common.Thermometer || device.DeviceType == common.ThermometerAndHygrometer {
			pd = publishedWirelessThermometer{device.WirelessThermometer}
		} else {
			pd = publishedDevice{
				dev:            *device,
				Address:        device.Address,
				Name:           ChoosePublishedDeviceName(device),
				RSSIData:       device.RSSIData,
				LastUpdateTime: device.LastUpdateTime,
			}
		}

		publishedDevices = append(publishedDevices, pd)
	}

	sort.Slice(publishedDevices,
		func(i int, j int) bool { return publishedDevices[i].SortKey() < publishedDevices[j].SortKey() })

	items := make([]interface{}, len(publishedDevices))

	for i, publishedDevice := range publishedDevices {
		items[i] = publishedDevice
	}

	p.state.container.RenderList(w, r, renderListOptions, items)
}

func ChoosePublishedDeviceName(dev *device) string {
	if dev.LocalName == "" {
		return dev.ReportedName
	}

	return dev.LocalName
}

func (p *plugin) Start() {
	fmt.Printf("%s Starting...\n", *p)

	p.state.container.RegisterEntityRenderer(
		reflect.TypeOf((*publishedDevice)(nil)).Elem(), p.bluetoothDeviceRendererFactory)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error)
	go p.run(ctx, runDone)

	p.state.scanCancelFunc = func() {
		cancel()
		err := <-runDone

		if err == nil {
			fmt.Printf("%s run completed\n", *p)
		} else {
			fmt.Printf("%s run completed with error: %s\n", *p, err)
		}
	}

	fmt.Printf("%s Started...\n", *p)
}

func (p *plugin) Stop() chan func() {
	fmt.Printf("%s Stopping...\n", *p)

	if p.state.scanCancelFunc != nil {
		fmt.Println("Stopping run...")
		scanCancelFunc := p.state.scanCancelFunc
		p.state.scanCancelFunc = nil

		fmt.Println("Waiting for run to stop...")
		scanCancelFunc()
	}

	fmt.Printf("%s Stopped\n", *p)

	return p.state.container.ClosedCallbackChannel()
}

func (p *plugin) run(ctx context.Context, runDone chan error) {
	// Register any pre-configured devices
	for _, dev := range p.state.devices {
		p.registerDevice(dev)

		if dev.DeviceType != common.Unknown {
			p.broadcastStateChangedEvent(dev)
		}
	}

	// Periodically run device discovery until we're asked to stop
	for {
		scanCtx, scanCtxCancelFunc := context.WithTimeout(ctx, 120*time.Second)
		err := p.doScan(scanCtx)
		scanCtxCancelFunc()

		var pause time.Duration = 5

		if err == nil {
			fmt.Printf(
				"doScan completed successfully, waiting %d seconds for next scan (unless we're done)\n", pause)
		} else {
			fmt.Printf("doScan failed, waiting 30 seconds for next attempt (unless we're done): %s\n", err)
		}

		if !wait(ctx, pause*time.Second) {
			fmt.Printf("wait returned false, completing run\n")
			close(runDone)
			break
		}
	}

	// Deregister any devices
	for _, dev := range p.state.devices {
		if dev.PmaasEntityId != "" {
			err := p.state.container.DeregisterEntity(dev.PmaasEntityId)

			if err == nil {
				fmt.Printf("Sucessfully deregistered %s (%s) with id %s\n", dev.Name, dev.Address, dev.PmaasEntityId)
			} else {
				fmt.Printf("Device %s (%s) with id %s could not be deregistered: %v\n",
					dev.Name, dev.Address, dev.PmaasEntityId, err)
			}
		}
	}

}

func (p *plugin) doScan(ctx context.Context) error {
	scanEventCh, scanCancelFunc, err := scanner.NewScanner().StartScan(p.state.adapterId)

	if err != nil {
		return fmt.Errorf("doScan failed, unable to start scanning: %w", err)
	}

	scanEventConsumerDone := make(chan error)

	// Kick off the event consumer
	go p.readScanEvents(scanEventCh, scanEventConsumerDone)

	// Wait until we're told to stop
	<-ctx.Done()
	fmt.Printf("doScan context is done, stopping scanner\n")

	// Cancel the scan
	scanCancelFunc()

	// And wait for the event consumer to finish
	fmt.Print("Waiting for readScanEvents to complete\n")
	err = <-scanEventConsumerDone
	if err == nil {
		fmt.Printf("ScanEvent reader stopped\n")
	} else {
		fmt.Printf("ScanEvent reader stopped with error: %s\n", err)
	}

	return nil
}

// A function to wait for the specified time, or until the context is done
func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return false

		case <-timer.C:
			return true
		}
	}
}

func (p *plugin) readScanEvents(scanEventCh chan *scanner.ScanEvent, doneCh chan error) {
	for evt := range scanEventCh {
		if evt == nil {
			fmt.Print("readScanEvents, received nil event from scanEventCh, exiting loop\n")
			break
		}

		fmt.Printf("readScanEvents, handling %s event\n", evt.Type)

		switch evt.Type {
		case scanner.EVENT_TYPE_DEVICE_DISCOVERED:
			p.handleDeviceDiscovered(evt)
			break
		case scanner.EVENT_TYPE_BATTERY_LEVEL_UPDATED:
			p.handleBatteryLevelUpdated(evt)
			break
		case scanner.EVENT_TYPE_ENVIRONMENT_DATA_UPDATED:
			p.handleEnvironmentDataUpdated(evt)
			break
		case scanner.EVENT_TYPE_RSSI_UPDATED:
			p.handleRSSIUpdated(evt)
			break
		case scanner.EVENT_TYPE_UUIDS_UPDATED:
			p.handleUUIDSUpdated(evt)
			break
		case scanner.EVENT_TYPE_DEVICE_TYPE_UPDATED:
			p.handleDeviceTypeUpdated(evt)
			break
		}

		fmt.Print("readScanEvents, bottom of scanEventCh message handling loop\n")
	}

	close(doneCh)
}

func (p *plugin) handleDeviceDiscovered(evt *scanner.ScanEvent) {
	dev, ok := p.state.devices[evt.Address]

	if ok {
		dev.LastUpdateTime = evt.EventTime
		dev.LastEventType = evt.Type

		// The device already existed, probably because of pre-registration.  Do we need
		// to update the discovery time?
		if dev.DeviceDiscoveryTime.IsZero() {
			dev.DeviceDiscoveryTime = evt.EventTime
		}

		// Ensure we have the UUID map
		if dev.UUIDs == nil {
			dev.UUIDs = make([]string, 0)
		}
	} else {
		// Avoid blowing up the device map with too many records.  This can happen when running
		// for long periods when devices with random addresses are present
		p.trimDevices()

		// We have not seen this device before, so create a record
		dev = &device{
			Id:                  evt.DeviceId,
			Address:             evt.Address,
			AddressType:         evt.AddressType,
			UUIDs:               make([]string, 0),
			DeviceDiscoveryTime: evt.EventTime,
			LastUpdateTime:      evt.EventTime,
		}
		p.state.devices[evt.Address] = dev
	}

	if dev.Id == "" {
		dev.Id = evt.DeviceId
	}

	dev.ReportedName = evt.Name

	if dev.LocalName == "" {
		dev.Name = dev.ReportedName
	} else {
		dev.Name = dev.LocalName
	}

	dev.RSSIData = environmental.RSSIData{
		RSSI:           evt.RSSI,
		LastUpdateTime: evt.EventTime,
	}

	addUUIDs(dev, evt.UUIDs)

	if evt.BatteryLevel != -1 {
		dev.BatteryData = environmental.BatteryData{
			Level:          evt.BatteryLevel,
			LastUpdateTime: evt.EventTime,
		}
	}

	if !evt.EnvironmentData.IsEmpty() {
		dev.SensorData = environmental.SensorData{
			Temperature:    evt.EnvironmentData.Temperature,
			HasHumidity:    evt.EnvironmentData.Humidity >= 0,
			Humidity:       evt.EnvironmentData.Humidity,
			LastUpdateTime: evt.EventTime,
		}
	}

	if evt.DeviceType != common.Unknown {
		p.applyDeviceType(dev, evt.DeviceType, evt.DeviceMakeAndModel)
	}

	if dev.DeviceType != common.Unknown {
		// Broadcast a state change if the device is now typed
		p.broadcastStateChangedEvent(dev)
	}
}

func (p *plugin) trimDevices() {
	if len(p.state.devices) < 100 {
		return
	}

	var lruDevice *device = nil

	for _, device := range p.state.devices {
		if device.LocalName != "" {
			// The device has a LocalName which means it was pre-registered and is not eligible for trimming.
			continue
		}

		// device.LastUpdateTime will never be nil because we always set it on creation as a result of device discovery
		if lruDevice == nil || device.LastUpdateTime.Before(lruDevice.LastUpdateTime) {
			lruDevice = device
		}
	}

	if lruDevice != nil {
		fmt.Printf("Trimming device %s (%s), last updated at %v from device map\n", lruDevice.Name, lruDevice.Address, lruDevice.LastUpdateTime)
		if lruDevice.PmaasEntityId != "" {
			err := p.state.container.DeregisterEntity(lruDevice.PmaasEntityId)

			if err != nil {
				fmt.Printf("Device %s (%s) could not be deregistered: %v\n", lruDevice.Name, lruDevice.Address, err)
			}
		}
		delete(p.state.devices, lruDevice.Address)
	}
}

func addUUIDs(dev *device, uuids []string) {
	for _, uuid := range uuids {
		if indexOf(uuid, dev.UUIDs) != -1 {
			continue
		}

		// If the uuids param contains duplicates, they'll be added into dev.UIIDs
		// since we don't detect them with this loop.
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

func (p *plugin) handleDeviceTypeUpdated(evt *scanner.ScanEvent) bool {
	dev, ok := p.state.devices[evt.Address]

	if !ok {
		fmt.Printf("Device %s not found, unable to process device type update\n", evt.Address)
		return false
	}

	if evt.DeviceType == common.Unknown {
		// Should not generally happen, but if it does there's nothing to update
		return true
	}

	if dev.DeviceType == evt.DeviceType {
		// We already have the correct type, but we may need to update the name
		if dev.DeviceMakeAndModel == "" {
			dev.DeviceMakeAndModel = evt.DeviceMakeAndModel
		}

		return true
	}

	// We're either going from unknown to a specific type, or the specific type is changing.
	p.applyDeviceType(dev, evt.DeviceType, evt.DeviceMakeAndModel)
	return true
}

func (p *plugin) handleEnvironmentDataUpdated(evt *scanner.ScanEvent) bool {
	dev, ok := p.state.devices[evt.Address]

	if !ok {
		fmt.Printf("Device %s not found, unable to process environment data update\n", evt.Address)
		return false
	}

	tempUpdated := dev.SensorData.Temperature != evt.EnvironmentData.Temperature
	humidityUpdated := dev.SensorData.Humidity != evt.EnvironmentData.Humidity

	dev.LastEventType = evt.Type
	dev.LastUpdateTime = evt.EventTime
	dev.SensorData = environmental.SensorData{
		Temperature:    evt.EnvironmentData.Temperature,
		HasHumidity:    evt.EnvironmentData.Humidity >= 0,
		Humidity:       evt.EnvironmentData.Humidity,
		LastUpdateTime: evt.EventTime,
	}

	if tempUpdated || humidityUpdated {
		p.broadcastStateChangedEvent(dev)
	}

	return true
}

func (p *plugin) handleRSSIUpdated(evt *scanner.ScanEvent) bool {
	dev, ok := p.state.devices[evt.Address]

	if !ok {
		fmt.Printf("Device %s not found, unable to process RSSI update\n", evt.Address)
		return false
	}

	rssiUpdated := dev.RSSIData.RSSI != evt.RSSI

	dev.LastEventType = evt.Type
	dev.LastUpdateTime = evt.EventTime
	dev.RSSIData = environmental.RSSIData{
		RSSI:           evt.RSSI,
		LastUpdateTime: evt.EventTime,
	}

	if rssiUpdated {
		//fmt.Printf("%T handleRSSIUpdated broadcastStateChangeEvent START\n", p)
		p.broadcastStateChangedEvent(dev)
		//fmt.Printf("%T handleRSSIUpdated broadcastStateChangeEvent END\n", p)
	}

	return true
}

func (p *plugin) handleUUIDSUpdated(evt *scanner.ScanEvent) bool {
	dev, ok := p.state.devices[evt.Address]

	if !ok {
		fmt.Printf("Device %s not found, unable to process UUIDs update\n", evt.Address)
		return false
	}

	dev.LastEventType = evt.Type
	dev.LastUpdateTime = evt.EventTime
	addUUIDs(dev, evt.UUIDs)
	return true
}

func (p *plugin) handleBatteryLevelUpdated(evt *scanner.ScanEvent) bool {
	dev, ok := p.state.devices[evt.Address]

	if !ok {
		fmt.Printf("Device %s not found, unable to process battery level update\n", evt.Address)
		return false
	}

	batteryLevelUpdated := dev.BatteryData.Level != evt.BatteryLevel

	dev.LastEventType = evt.Type
	dev.LastUpdateTime = evt.EventTime
	dev.BatteryData = environmental.BatteryData{
		Level:          evt.BatteryLevel,
		LastUpdateTime: evt.EventTime,
	}

	if batteryLevelUpdated {
		p.broadcastStateChangedEvent(dev)
	}

	return true
}

func (p *plugin) applyDeviceType(dev *device, deviceType common.DeviceType, makeAndModel string) {
	dev.DeviceType = deviceType
	dev.DeviceMakeAndModel = makeAndModel

	if dev.PmaasEntityId == "" {
		p.registerDevice(dev)
	}
	// We'll need to create or update global entity registration
}

func (p *plugin) registerDevice(dev *device) {
	entityType := getEntityType(dev.DeviceType)
	id, err := p.state.container.RegisterEntity(dev.Address, entityType, dev.LocalName, nil)

	if err != nil {
		fmt.Printf("Device %s could not be registered: %v\n", dev.Address, err)
		return
	}

	dev.PmaasEntityId = id
}

func getEntityType(deviceType common.DeviceType) reflect.Type {
	if deviceType == common.Thermometer || deviceType == common.ThermometerAndHygrometer {
		return IWirelessThermometerType
	}

	return publishedDeviceType
}

func (p *plugin) bluetoothDeviceRendererFactory() (spi.EntityRenderer, error) {
	// Load the template
	t, err := p.state.container.GetTemplate(&bluetoothDeviceTemplate)

	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("unable to load bluetooth_device template: %v", err)
	}

	// Create a function that casts the entity to the expected type and evaluates it via the template loaded above
	renderer := func(w io.Writer, entity any) error {
		wt, ok := entity.(publishedDevice)

		if !ok {
			return errors.New("item is not an instance of publishedDevice")
		}

		err := t.Instance.Execute(w, wt)

		if err != nil {
			return fmt.Errorf("unable to execute bluetooth_device template: %w", err)
		}

		return nil
	}

	return spi.EntityRenderer{StreamingRenderFunc: renderer, Styles: t.Styles, Scripts: t.Scripts}, nil
}

func (p *plugin) broadcastStateChangedEvent(dev *device) {
	event := events.EntityStateChangedEvent{
		EntityEvent: events.EntityEvent{
			Id:         dev.PmaasEntityId,
			EntityType: getEntityType(dev.DeviceType),
		},
		NewState: dev.WirelessThermometer,
	}

	err := p.state.container.BroadcastEvent(dev.PmaasEntityId, event)

	if err != nil {
		fmt.Printf("Failed to broadcast EntityStateChangedEvent: %v", err)
	}

}

func RelativeTime(timeValue time.Time) string {
	elapsed := time.Now().Sub(timeValue).Truncate(time.Second)

	if elapsed.Seconds() < 30 {
		return "< 30s"
	}

	if elapsed.Seconds() < 60 {
		return "< 1m"
	}

	elapsed = elapsed.Truncate(time.Minute)

	if elapsed.Minutes() < 60 {
		return fmt.Sprintf("%vm", elapsed.Minutes())
	}

	elapsed = elapsed.Truncate(time.Hour)

	return fmt.Sprintf("%vh", elapsed.Hours())
}
