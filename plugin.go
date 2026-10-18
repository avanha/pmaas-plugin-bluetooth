package bluetooth

import (
	"embed"
	"errors"
	"fmt"
	"github.com/avanha/pmaas-spi/logging"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"text/template"
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/parser"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/bluetooth"
	"github.com/avanha/pmaas-spi/clock"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
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

	// temporary is that the address is one the device will change (see bluetooth.IsTemporaryAddress), so it is forgotten
	// sooner than a device that keeps its own.
	temporary bool

	// known is the name and services advertised so far, merged: the name comes in one advertisement and the reading in another.
	// It is what the device is recognised by. Readings are not kept: for some devices each is a new key.
	known bluetooth.Fields

	// parser is what decodes the device's advertisements, once it is known what the device is.
	parser *parser.Parser

	// signals is how each source hears the device, by the source's entity ID.
	signals map[string]signal
}

// signal is how loudly one source last heard a device.
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

	// configured are the addresses the assembly named, which the sources are asked for whatever else is wanted.
	configured []string

	// receivers are the handles of the event receivers registered, to be given back when the plugin stops.
	receivers []int

	// listAll is whether to ask the sources for sightings of every device they hear.
	listAll bool

	// beaconButtons are the buttons the assembly named, and buttons the ones being watched, by address.
	beaconButtons []*BeaconButton
	buttons       map[string]*button

	now func() time.Time
}

type BluetoothPlugin interface {
	spi.IPMAASPlugin
}

func NewPlugin(config PluginConfig) BluetoothPlugin {
	instance := &plugin{
		devices:       make(map[string]*device),
		now:           time.Now,
		listAll:       config.ListAllDevices,
		beaconButtons: config.buttons,
		buttons:       make(map[string]*button),
	}

	for _, configured := range config.buttons {
		if len(configured.frames) == 0 {
			panic(fmt.Sprintf("bluetooth: the button %q (%s) needs at least one press: say which frame is Single, Double, Triple or Long",
				configured.name, configured.address))
		}
	}

	for address, dev := range config.devices {
		instance.devices[address] = &device{
			Address:    dev.Address,
			LocalName:  dev.LocalName,
			DeviceType: dev.DeviceType,
			WirelessThermometer: environmental.WirelessThermometer{
				Name: dev.LocalName,
			},
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
					HasData:        true,
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

func (p *plugin) ShortName() string {
	return "bluetooth"
}

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.container = container
	p.log = logging.For(container)
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
		reflect.TypeOf((*publishedDevice)(nil)).Elem(), p.bluetoothDeviceRendererFactory)

	p.container.RegisterEntityRenderer(publishedBeaconType, p.beaconRendererFactory)

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

	p.stopButtons()

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
