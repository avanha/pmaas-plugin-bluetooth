package bluetooth

import (
	"embed"
	"errors"
	"fmt"
	"github.com/avanha/pmaas-spi/format"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"text/template"
	"time"

	"github.com/avanha/pmaas-spi/logging"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/bluetooth"
	"github.com/avanha/pmaas-spi/clock"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
	"github.com/avanha/pmaas-spi/group"
)

//go:embed content/static content/templates
var contentFS embed.FS

var bluetoothDeviceTemplate = spi.TemplateInfo{
	Name: "bluetooth_device",
	FuncMap: template.FuncMap{
		"RelativeTime": format.Terse,
	},
	Paths:  []string{"templates/bluetooth_device.htmlt"},
	Styles: []string{"css/bluetooth_device.css"},
}

var IWirelessThermometerType = reflect.TypeFor[environmental.IWirelessThermometer]()

// EnhancedSensorData Extend the spi/environment/SensorData with a locally calculated Farenheit value
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

	// Indicates that the device is using a temporary MAC address.  Devices with temporary addresses age out sooner.
	temporary bool

	// viewGiven is that the device's entity has a view (see giveView).
	viewGiven bool

	// Known is accumulated data about the device, including its name and services advertised so far.  The data is
	// merged across multiple advertisements, since the name can come in one, while readings in another.  This is how
	// we recognize devices. Readings are not kept because for some devices each reading is a new key.
	known bluetooth.Fields

	// model is what identified the device, if a model did, and impl the device's own state, which decodes its advertisements once it
	// is identified or claimed. disabled is that impl panicked, so is not heard any more. claimChecked is that the configured devices
	// that claim by more than a fixed address have been asked about it.
	model        bluetooth.DeviceModel
	impl         bluetooth.Device
	disabled     bool
	claimChecked bool

	// Signals tracks how each source hears the device, by the source's entity ID.
	signals map[string]signal
}

// Signal captures the RSSI and time of the most recent advertisement.
type signal struct {
	rssi int
	at   time.Time
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

	// Temporary is that the address is one the device will change, so it is not one to configure a device by.
	Temporary bool
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

// plugin takes the advertisements the sources hear, works out what the devices are, and publishes them. Everything it owns
// belongs to the plugin goroutine: advertisements are delivered to it there, and the page asks it for a snapshot.
type plugin struct {
	log       *slog.Logger
	container spi.IPMAASContainer
	devices   map[string]*device

	// models are what the assembly asked to have recognised, configuredDevices what it configured, buttonDevices the ones that are
	// buttons (watched from the start, by address), and claimers those that claim by more than a fixed address.
	models            []bluetooth.DeviceModel
	configuredDevices []bluetooth.ConfiguredDevice
	buttonDevices     []bluetooth.ConfiguredDevice
	claimers          []bluetooth.ConfiguredDevice

	// configured are the addresses of thermometers the assembly named only by address, which the sources are asked for whatever
	// else is wanted.
	configured []string

	// receivers are the handles of the event receivers registered, to be given back when the plugin stops.
	receivers []int

	// listAll is whether to ask the sources for sightings of every device they hear, and activeScan whether to ask them to scan actively.
	listAll    bool
	activeScan bool

	// scanEvery and scanFor are how often and how long to scan actively when a configured device needs scan responses
	// (needsScanResponses). windowOpen is whether the scan is active for that reason now, and lastWindow when the last window
	// opened. stopWindow ends the timer that closes the window. after runs a function on the plugin goroutine after a delay, and
	// returns what cancels it; tests replace it.
	scanEvery, scanFor time.Duration
	needsScanResponses bool
	windowOpen         bool
	lastWindow         time.Time
	stopWindow         func()
	after              func(d time.Duration, f func()) (cancel func())

	// buttons are the buttons being watched, by address.
	buttons map[string]*button

	// handles are the assembly's handles on thermometers, by address.
	handles map[string]*ThermometerHandle

	now func() time.Time
}

type BluetoothPlugin interface {
	spi.IPMAASPlugin
}

func NewPlugin(config PluginConfig) BluetoothPlugin {
	instance := &plugin{
		devices:           make(map[string]*device),
		now:               time.Now,
		listAll:           config.ListAllDevices,
		activeScan:        config.ActiveScan,
		scanEvery:         config.ActiveScanEvery,
		scanFor:           config.ActiveScanFor,
		handles:           config.handles,
		models:            config.models,
		configuredDevices: config.added,
		buttons:           make(map[string]*button),
	}

	for _, configured := range config.added {
		if err := validateConfigured(configured); err != nil {
			panic(fmt.Sprintf("bluetooth: %v", err))
		}

		if configured.NeedsScanResponse() {
			instance.needsScanResponses = true
		}

		addressed, isAddressed := configured.(bluetooth.Addressed)

		switch {
		case configured.Kind() == bluetooth.KindButton:
			instance.buttonDevices = append(instance.buttonDevices, configured)
		case isAddressed:
			dev := &device{
				Address:      addressed.Address(),
				Name:         configured.Name(),
				LocalName:    configured.Name(),
				claimChecked: true,
			}

			instance.adopt(dev, configured)
			instance.devices[dev.Address] = dev
		default:
			instance.claimers = append(instance.claimers, configured)
		}
	}

	for address, dev := range config.devices {
		instance.devices[address] = &device{
			Address:      dev.Address,
			LocalName:    dev.LocalName,
			DeviceType:   dev.DeviceType,
			Name:         dev.LocalName,
			claimChecked: true,
		}

		instance.configured = append(instance.configured, address)
	}

	sort.Strings(instance.configured)

	if config.EnableTestDevices {
		testDeviceAddress := "AA:BB:CC:DD:EE:FF"
		now := time.Now()
		instance.devices[testDeviceAddress] = &device{
			Address:   "AA:BB:CC:DD:EE:FF",
			LocalName: "Test Thermometer",

			DeviceType: common.ThermometerAndHygrometer,

			// WirelessThermometer fields:
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
				HasData:        true,
				Temperature:    25.73,
				HasHumidity:    true,
				Humidity:       45.25,
				LastUpdateTime: now,
			},
			LastUpdateTime: now,
		}
	}

	return instance
}

// Implementation of spi.IPMAASPlugin
var _ spi.IPMAASPlugin = (*plugin)(nil)

func (p *plugin) ShortName() string {
	return "bluetooth"
}

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.container = container
	p.log = logging.For(container)
	p.after = func(d time.Duration, f func()) func() {
		timer := time.AfterFunc(d, func() {
			if err := container.EnqueueOnPluginGoRoutine(f); err != nil {
				p.log.Error("unable to close the active scan window", "error", err)
			}
		})

		return func() { timer.Stop() }
	}
	for address, handle := range p.handles {
		handle.attach(container, func() environmental.WirelessThermometer {
			if dev, ok := p.devices[address]; ok {
				return dev.WirelessThermometer
			}

			return environmental.WirelessThermometer{}
		})
	}

	container.ProvideContentFS(&contentFS, "content")
	container.EnableStaticContent("static")
	container.AddRoute("", p.HandleHttpList)
}

var renderListOptions = spi.RenderListOptions{Title: "Bluetooth Devices"}

func (p *plugin) HandleHttpList(w http.ResponseWriter, r *http.Request) {
	// Asked for on the plugin goroutine, which owns the devices.
	items, err := spi.Exec(p.container, p.listItems)
	if err != nil {
		p.log.Error("unable to list the devices", "error", err)
		items = nil
	}

	p.container.RenderList(w, r, renderListOptions, items)
}

// listItems is what the page shows. Runs on the plugin goroutine.
func (p *plugin) listItems() []interface{} {
	publishedDevices := make([]Sortable, 0)

	for _, b := range p.buttons {
		publishedDevices = append(publishedDevices, b.publish())
	}

	for _, device := range p.devices {
		if device.ReportedName == "" && device.LocalName == "" {
			continue
		}

		// A beacon button is listed as one, with what it is for.
		if _, isButton := p.buttons[device.Address]; isButton {
			continue
		}

		var pd Sortable

		if isThermometer(device.DeviceType) {
			pd = publishedWirelessThermometer{device.WirelessThermometer}
		} else {
			pd = publishedDevice{
				dev:            *device,
				Address:        device.Address,
				Name:           ChoosePublishedDeviceName(device),
				RSSIData:       device.RSSIData,
				LastUpdateTime: device.LastUpdateTime,
				Temporary:      device.temporary,
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

	return items
}

func ChoosePublishedDeviceName(dev *device) string {
	if dev.LocalName == "" {
		return dev.ReportedName
	}

	return dev.LocalName
}

func (p *plugin) Start() {
	p.log.Info("starting")

	p.container.RegisterEntityRenderer(
		reflect.TypeFor[publishedDevice](), p.bluetoothDeviceRendererFactory)

	p.container.RegisterEntityRenderer(publishedBeaconType, p.beaconRendererFactory)
	p.container.RegisterEntityRenderer(publishedThermometerType, p.thermometerRendererFactory)

	p.startButtons()

	// Register any pre-configured devices
	for _, dev := range p.devices {
		p.registerDevice(dev)

		if dev.DeviceType != common.Unknown {
			p.broadcastStateChangedEvent(dev)
		}
	}

	// The advertisements the sources forward, the sightings of the devices they hear that nobody wants, and the minute, when
	// what is wanted is said again and the devices that have gone are let go.
	for _, r := range []struct {
		predicate events.EventPredicate
		receiver  events.EventReceiver
		what      string
	}{
		{bluetooth.AdvertisementPredicate, p.onAdvertisement, "advertisements"},
		{bluetooth.SightingPredicate, p.onSighting, "sightings"},
		{isMinuteChanged, p.onMinute, "the clock"},
	} {
		handle, err := p.container.RegisterEventReceiver(r.predicate, r.receiver)
		if err != nil {
			p.log.Error("unable to listen", "what", r.what, "error", err)

			continue
		}

		p.receivers = append(p.receivers, handle)
	}

	p.updateScanWindow(p.now())
	p.publishInterest()

	p.log.Info("started")
}

// Stop runs on the plugin goroutine. There is nothing to wait for: the sources are other plugins, and the advertisements
// arrive on this goroutine.
func (p *plugin) Stop() chan func() {
	p.log.Info("stopping")

	if len(p.receivers) > 0 {
		for _, handle := range p.receivers {
			if err := p.container.DeregisterEventReceiver(handle); err != nil {
				p.log.Error("unable to stop listening", "error", err)
			}
		}

		p.receivers = nil

		// Nothing is wanted any more.
		if err := bluetooth.PublishInterest(p.container, p.ShortName(), nil, false); err != nil {
			p.log.Error("unable to say that nothing is wanted", "error", err)
		}
	}

	p.closeScanWindow()
	p.stopButtons()

	for _, handle := range p.handles {
		handle.detach()
		handle.setEntityId("")
	}

	for _, dev := range p.devices {
		if dev.PmaasEntityId != "" {
			err := p.container.DeregisterEntity(dev.PmaasEntityId)

			if err == nil {
				p.log.Debug("deregistered device", "name", dev.Name, "address", dev.Address, "pmaasEntityId", dev.PmaasEntityId)
			} else {
				p.log.Error("device could not be deregistered", "name", dev.Name, "address", dev.Address, "pmaasEntityId", dev.PmaasEntityId, "error", err)
			}
		}
	}

	return p.container.ClosedCallbackChannel()
}

func isMinuteChanged(eventInfo *events.EventInfo) bool {
	_, ok := eventInfo.Event.(clock.MinuteChangedEvent)

	return ok
}

func (p *plugin) onMinute(*events.EventInfo) error {
	p.updateScanWindow(p.now())
	p.publishInterest()
	p.ageDevices(p.now())

	return nil
}

func (p *plugin) bluetoothDeviceRendererFactory() (spi.EntityRenderer, error) {
	// Load the template
	t, err := p.container.GetTemplate(&bluetoothDeviceTemplate)

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

func (p *plugin) registerDevice(dev *device) {
	entityType := getEntityType(dev.DeviceType)
	id, err := p.container.RegisterEntity(dev.Address, entityType, dev.LocalName, nil)

	if err != nil {
		p.log.Error("device could not be registered", "address", dev.Address, "error", err)
		return
	}

	dev.PmaasEntityId = id

	if handle := p.handles[dev.Address]; handle != nil {
		handle.setEntityId(id)
	}

	p.giveView(dev)
}

// giveView gives a thermometer's entity the view of its card, for a page that lists the entities of many plugins. Other devices are
// not shown on such a page: what is heard nearby is not what a house is made of. It is done once the device is known to be a
// thermometer, which may be after its entity was registered.
func (p *plugin) giveView(dev *device) {
	if dev.PmaasEntityId == "" || dev.viewGiven || !isThermometer(dev.DeviceType) {
		return
	}

	address := dev.Address

	err := p.container.RegisterEntityView(dev.PmaasEntityId, func() (spi.EntityView, error) {
		current, ok := p.devices[address]
		if !ok {
			return spi.EntityView{}, errors.New("the thermometer is no longer known")
		}

		return spi.EntityView{
			Key: "bluetooth/" + address, Category: group.CategoryThermometers, Value: publishedWirelessThermometer{current.WirelessThermometer},
		}, nil
	})
	if err != nil {
		p.log.Error("unable to register the view of a thermometer", "address", address, "error", err)

		return
	}

	dev.viewGiven = true
}

func isThermometer(deviceType common.DeviceType) bool {
	return deviceType == common.Thermometer || deviceType == common.ThermometerAndHygrometer
}

func getEntityType(deviceType common.DeviceType) reflect.Type {
	if deviceType == common.Thermometer || deviceType == common.ThermometerAndHygrometer {
		return IWirelessThermometerType
	}

	return publishedDeviceType
}

func (p *plugin) broadcastStateChangedEvent(dev *device) {
	event := events.EntityStateChangedEvent{
		EntityEvent: events.EntityEvent{
			Id:         dev.PmaasEntityId,
			EntityType: getEntityType(dev.DeviceType),
		},
		NewState: dev.WirelessThermometer,
	}

	err := p.container.BroadcastEvent(dev.PmaasEntityId, event)

	if err != nil {
		p.log.Error("failed to broadcast EntityStateChangedEvent", "error", err)
	}
}
