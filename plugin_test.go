package bluetooth

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/common"
	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/bluetooth"
	"github.com/avanha/pmaas-spi/clock"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
)

// Advertisements captured from a Bluetooth proxy, from sensors that are ours.
const (
	goveeFrame           = "0d09475648353037355f45324630030388ec02010509ff88ec000339a664001aff4c000215494e54454c4c495f524f434b535f48575075f2ff0c"
	goveeFrameLowBattery = "0d09475648353037355f45324630030388ec02010509ff88ec000339a650001aff4c000215494e54454c4c495f524f434b535f48575075f2ff0c"
	goveeFrame2          = "0d09475648353037355f45324630030388ec02010509ff88ec000339bb64001aff4c000215494e54454c4c495f524f434b535f48575075f2ff0c"
	inkbirdFrame         = "0201060302f0ff04097370730aff6607970f0099606408"
	goveeAddress         = "A4:C1:38:CC:E2:F0"
	inkbirdAddres        = "49:22:01:23:0A:1B"
)

type receiver struct {
	predicate events.EventPredicate
	receive   events.EventReceiver
}

type fakeContainer struct {
	spi.IPMAASContainer

	mu           sync.Mutex
	entities     map[string]reflect.Type
	entityIds    []string
	deregistered []string
	receivers    map[int]receiver
	off          []int
	broadcasts   []any
	listed       []interface{}
}

func newFakeContainer() *fakeContainer {
	return &fakeContainer{entities: map[string]reflect.Type{}, receivers: map[int]receiver{}}
}

func (c *fakeContainer) ProvideContentFS(fs.FS, string)                                 {}
func (c *fakeContainer) EnableStaticContent(string)                                     {}
func (c *fakeContainer) AddRoute(string, http.HandlerFunc)                              {}
func (c *fakeContainer) RegisterEntityRenderer(reflect.Type, spi.EntityRendererFactory) {}

func (c *fakeContainer) RegisterEntity(uniqueData string, entityType reflect.Type, _ string, _ spi.EntityStubFactoryFunc) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := fmt.Sprintf("bluetooth_%d", len(c.entityIds)+1)
	c.entityIds = append(c.entityIds, id)
	c.entities[uniqueData] = entityType

	return id, nil
}

func (c *fakeContainer) DeregisterEntity(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.deregistered = append(c.deregistered, id)

	return nil
}

func (c *fakeContainer) RegisterEventReceiver(predicate events.EventPredicate, r events.EventReceiver) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	handle := len(c.receivers) + len(c.off) + 1
	c.receivers[handle] = receiver{predicate, r}

	return handle, nil
}

func (c *fakeContainer) DeregisterEventReceiver(handle int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.receivers, handle)
	c.off = append(c.off, handle)

	return nil
}

func (c *fakeContainer) BroadcastEvent(_ string, event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.broadcasts = append(c.broadcasts, event)

	return nil
}

func (c *fakeContainer) EnqueueOnPluginGoRoutine(f func()) error {
	f()

	return nil
}

func (c *fakeContainer) RenderList(_ http.ResponseWriter, _ *http.Request, _ spi.RenderListOptions, items []interface{}) {
	c.listed = items
}

func (c *fakeContainer) ClosedCallbackChannel() chan func() {
	ch := make(chan func())
	close(ch)

	return ch
}

// deliver sends the event to the receivers that want it, as the event manager would.
func (c *fakeContainer) deliver(event any) {
	c.mu.Lock()
	var wanting []events.EventReceiver

	for _, r := range c.receivers {
		if r.predicate(&events.EventInfo{Event: event}) {
			wanting = append(wanting, r.receive)
		}
	}
	c.mu.Unlock()

	for _, receive := range wanting {
		if err := receive(&events.EventInfo{Event: event}); err != nil {
			panic(err)
		}
	}
}

func (c *fakeContainer) states() []events.EntityStateChangedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []events.EntityStateChangedEvent

	for _, b := range c.broadcasts {
		if e, ok := b.(events.EntityStateChangedEvent); ok {
			result = append(result, e)
		}
	}

	return result
}

func (c *fakeContainer) interests() []bluetooth.InterestEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []bluetooth.InterestEvent

	for _, b := range c.broadcasts {
		if e, ok := b.(bluetooth.InterestEvent); ok {
			result = append(result, e)
		}
	}

	return result
}

var epoch = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t         *testing.T
	container *fakeContainer
	plugin    *plugin
	clock     time.Time
}

func newFixture(t *testing.T, config PluginConfig) *fixture {
	t.Helper()

	f := &fixture{t: t, container: newFakeContainer(), clock: epoch}
	f.plugin = NewPlugin(config).(*plugin)
	f.plugin.now = func() time.Time { return f.clock }
	f.plugin.Init(f.container)
	f.plugin.Start()

	return f
}

func (f *fixture) advance(d time.Duration) { f.clock = f.clock.Add(d) }

// hear delivers an advertisement from a source, as the event manager would.
func (f *fixture) hear(source string, address string, rssi int, frame string) {
	f.t.Helper()

	raw, err := hex.DecodeString(frame)
	if err != nil {
		f.t.Fatal(err)
	}

	fields, err := bluetooth.ParseAdvertisingData(raw)
	if err != nil {
		f.t.Fatal(err)
	}

	f.container.deliver(bluetooth.AdvertisementEvent{
		Source: source, Address: address, AddressType: bluetooth.AddressPublic, RSSI: rssi, Time: f.clock, Fields: fields, Raw: raw,
	})
}

func (f *fixture) device(address string) *device {
	f.t.Helper()

	d, ok := f.plugin.devices[address]
	if !ok {
		f.t.Fatalf("no device %s", address)
	}

	return d
}

func lastState(t *testing.T, c *fakeContainer) environmental.WirelessThermometer {
	t.Helper()

	states := c.states()
	if len(states) == 0 {
		t.Fatal("no state was broadcast")
	}

	return states[len(states)-1].NewState.(environmental.WirelessThermometer)
}

func TestStart_RegistersWhatWasConfiguredListensAndSaysWhatIsWanted(t *testing.T) {
	config := NewPluginConfig()
	config.AddThermometer("a4-c1-38-00-00-01", "Garage")
	f := newFixture(t, config)

	if f.container.entities["A4:C1:38:00:00:01"] != IWirelessThermometerType {
		t.Errorf("entities %v", f.container.entities)
	}

	if states := f.container.states(); len(states) != 1 || states[0].EntityType != IWirelessThermometerType {
		t.Errorf("states %+v", states)
	}

	if len(f.container.receivers) != 2 {
		t.Fatalf("%d receivers", len(f.container.receivers))
	}

	interests := f.container.interests()
	if len(interests) != 1 || interests[0].Owner != "bluetooth" || !interests[0].ActiveScan {
		t.Fatalf("interests %+v", interests)
	}

	// What the parsers recognise, and the configured device by its address.
	names := map[string]bool{}
	for _, r := range interests[0].Rules {
		names[r.Name] = true
	}

	if !names["govee-h5075"] || !names["inkbird-ibs-th"] || !names["configured A4:C1:38:00:00:01"] || len(names) != 3 {
		t.Errorf("rules %v", names)
	}

	if err := bluetooth.PublishInterest(f.container, "x", interests[0].Rules, false); err != nil {
		t.Errorf("what is wanted is not valid: %v", err)
	}
}

func TestInterest_IsSaidAgainEveryMinuteAndWithdrawnWhenItStops(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.container.deliver(clock.MinuteChangedEvent{Time: epoch})
	f.container.deliver(clock.MinuteChangedEvent{Time: epoch.Add(time.Minute)})

	if got := len(f.container.interests()); got != 3 {
		t.Fatalf("%d interests", got)
	}

	<-f.plugin.Stop()

	interests := f.container.interests()
	if last := interests[len(interests)-1]; len(last.Rules) != 0 {
		t.Errorf("not withdrawn: %+v", last)
	}

	if len(f.container.receivers) != 0 {
		t.Errorf("still listening: %v", f.container.receivers)
	}
}

func TestAdvertisement_AGoveeIsRecognisedRegisteredAndReported(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.hear("src1", goveeAddress, -61, goveeFrame)

	if f.container.entities[goveeAddress] != IWirelessThermometerType {
		t.Fatalf("entities %v", f.container.entities)
	}

	state := lastState(t, f.container)
	if state.SensorData.Temperature != 21.1 || state.SensorData.Humidity != 36.6 || !state.SensorData.HasHumidity ||
		state.BatteryData.Level != 100 || state.RSSIData.RSSI != -61 || state.Name != "GVH5075_E2F0" {
		t.Errorf("state %+v", state)
	}

	d := f.device(goveeAddress)
	if d.DeviceMakeAndModel != "Govee GVH5075" || d.DeviceType != common.ThermometerAndHygrometer || d.AddressType != "public" ||
		!d.DeviceDiscoveryTime.Equal(epoch) {
		t.Errorf("device %+v", d)
	}
}

func TestAdvertisement_AnInkbirdIsRecognisedByItsNameAndService(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.hear("src1", inkbirdAddres, -90, inkbirdFrame)

	state := lastState(t, f.container)
	if state.SensorData.Temperature != 18.94 || state.SensorData.Humidity != 39.91 || state.BatteryData.Level != 100 || state.Name != "sps" {
		t.Errorf("state %+v", state)
	}

	if f.device(inkbirdAddres).DeviceMakeAndModel != "Inkbird IBS-TH" {
		t.Errorf("%+v", f.device(inkbirdAddres))
	}
}

func TestAdvertisement_AnAdvertisementThatChangesNothingIsNotBroadcastAgainButOneThatDoesIs(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.hear("src1", goveeAddress, -61, goveeFrame)

	f.advance(time.Minute)
	f.hear("src1", goveeAddress, -61, goveeFrame)

	if got := len(f.container.states()); got != 1 {
		t.Fatalf("%d states after a repeat", got)
	}

	// But it was heard.
	if got := f.device(goveeAddress).LastUpdateTime; !got.Equal(epoch.Add(time.Minute)) {
		t.Errorf("last update %v", got)
	}

	f.advance(time.Minute)
	f.hear("src1", goveeAddress, -61, goveeFrame2) // 0x339bb: 21.1 + a little, another humidity

	if got := len(f.container.states()); got != 2 {
		t.Fatalf("%d states after a new reading", got)
	}

	if state := lastState(t, f.container); state.SensorData.Humidity == 36.6 {
		t.Errorf("state %+v", state)
	}

	f.advance(time.Minute)
	f.hear("src1", goveeAddress, -70, goveeFrame2) // only the signal

	if got := len(f.container.states()); got != 3 {
		t.Fatalf("%d states after a change of signal", got)
	}

	if lastState(t, f.container).RSSIData.RSSI != -70 {
		t.Error("the signal is not the new one")
	}
}

func TestAdvertisement_ABatteryThatRunsDownIsReportedEvenWhenTheReadingIsTheSame(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.hear("src1", goveeAddress, -61, goveeFrame)
	f.advance(time.Minute)
	f.hear("src1", goveeAddress, -61, goveeFrameLowBattery)

	if got := len(f.container.states()); got != 2 {
		t.Fatalf("%d states", got)
	}

	if state := lastState(t, f.container); state.BatteryData.Level != 0x50 || state.SensorData.Temperature != 21.1 {
		t.Errorf("state %+v", state)
	}
}

func TestAdvertisement_TheStrongestSourceThatHasHeardLatelyIsTheSignal(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	f.hear("near", goveeAddress, -50, goveeFrame)
	f.advance(10 * time.Second)
	f.hear("far", goveeAddress, -85, goveeFrame)

	if got := f.device(goveeAddress).RSSIData; got.RSSI != -50 {
		t.Errorf("the weaker source took over: %+v", got)
	}

	// The near one goes quiet; after a minute only the far one counts.
	f.advance(61 * time.Second)
	f.hear("far", goveeAddress, -84, goveeFrame)

	if got := f.device(goveeAddress).RSSIData; got.RSSI != -84 {
		t.Errorf("a source not heard from lately counted: %+v", got)
	}

	// A source not heard for a long time is forgotten altogether.
	f.advance(11 * time.Minute)
	f.hear("far", goveeAddress, -80, goveeFrame)

	if _, kept := f.device(goveeAddress).signals["near"]; kept {
		t.Error("the long-silent source was kept")
	}
}

func TestAdvertisement_ADeviceNobodyRecognisedIsKeptButNotRegisteredOrBroadcastAndIsListedByName(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	raw := []byte{0x06, 0x09, 'P', 'h', 'o', 'n', 'e'}
	fields, _ := bluetooth.ParseAdvertisingData(raw)
	f.container.deliver(bluetooth.AdvertisementEvent{Source: "s", Address: "11:22:33:44:55:66", RSSI: -80, Time: epoch, Fields: fields})

	if len(f.container.entities) != 0 || len(f.container.states()) != 0 {
		t.Fatalf("entities %v, states %v", f.container.entities, f.container.states())
	}

	f.hear("s", goveeAddress, -61, goveeFrame)

	f.plugin.HandleHttpList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if len(f.container.listed) != 2 {
		t.Fatalf("listed %d", len(f.container.listed))
	}

	// Sorted by name: the thermometer is "GVH5075...", the other "Phone".
	if _, ok := f.container.listed[0].(publishedWirelessThermometer); !ok {
		t.Errorf("first %T", f.container.listed[0])
	}

	if p, ok := f.container.listed[1].(publishedDevice); !ok || p.Name != "Phone" || p.Address != "11:22:33:44:55:66" {
		t.Errorf("second %+v", f.container.listed[1])
	}
}

func TestAdvertisement_ANamedConfiguredDeviceKeepsItsNameAndItsEntity(t *testing.T) {
	config := NewPluginConfig()
	config.AddThermometer(goveeAddress, "Garage")
	f := newFixture(t, config)

	f.hear("s", goveeAddress, -61, goveeFrame)

	if len(f.container.entityIds) != 1 {
		t.Fatalf("%d entities for one device", len(f.container.entityIds))
	}

	state := lastState(t, f.container)
	if state.Name != "Garage" || state.SensorData.Temperature != 21.1 {
		t.Errorf("state %+v", state)
	}

	if d := f.device(goveeAddress); d.ReportedName != "GVH5075_E2F0" || d.LocalName != "Garage" || d.PmaasEntityId != "bluetooth_1" {
		t.Errorf("device %+v", d)
	}
}

func TestAdvertisement_ADevicesAdvertisementsAreMergedSoANameAndAReadingFromDifferentOnesBothCount(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	// The service and name first, with no reading.
	f.container.deliver(bluetooth.AdvertisementEvent{
		Source: "s", Address: goveeAddress, RSSI: -60, Time: epoch,
		Fields: bluetooth.Fields{LocalName: "GVH5075_E2F0", ServiceUUIDs: []string{bluetooth.UUID16(0xec88)}},
	})

	if f.device(goveeAddress).DeviceType != common.ThermometerAndHygrometer {
		t.Fatal("not recognised by its name and service alone")
	}

	// Then a reading with neither.
	f.container.deliver(bluetooth.AdvertisementEvent{
		Source: "s", Address: goveeAddress, RSSI: -60, Time: epoch,
		Fields: bluetooth.Fields{ManufacturerData: map[uint16][]byte{0xec88: {0x00, 0x03, 0x39, 0xa6, 0x64, 0x00}}},
	})

	state := lastState(t, f.container)
	if state.SensorData.Temperature != 21.1 || state.Name != "GVH5075_E2F0" {
		t.Errorf("state %+v", state)
	}
}

func TestAdvertisement_OnlyTheDevicesWithoutANameOfTheirOwnAreTrimmedAndTheyAreDeregistered(t *testing.T) {
	config := NewPluginConfig()
	config.AddThermometer("A4:C1:38:00:00:01", "Garage")
	f := newFixture(t, config)

	// The oldest is a thermometer nobody named, which is registered, so trimming it deregisters it.
	f.hear("s", goveeAddress, -61, goveeFrame)

	for i := 0; i < maxDevices+5; i++ {
		f.advance(time.Second)
		f.container.deliver(bluetooth.AdvertisementEvent{
			Source: "s", Address: bluetooth.FormatAddress(uint64(0xB00000000000 + i)), Time: f.clock,
			Fields: bluetooth.Fields{LocalName: fmt.Sprintf("D%d", i)},
		})
	}

	if len(f.plugin.devices) > maxDevices {
		t.Errorf("%d devices", len(f.plugin.devices))
	}

	if _, kept := f.plugin.devices["A4:C1:38:00:00:01"]; !kept {
		t.Error("the configured device was trimmed")
	}

	if _, kept := f.plugin.devices[goveeAddress]; kept {
		t.Error("the oldest was kept")
	}

	if !reflect.DeepEqual(f.container.deregistered, []string{"bluetooth_2"}) {
		t.Errorf("deregistered %v", f.container.deregistered)
	}
}

func TestAdvertisement_AnythingElseIsIgnored(t *testing.T) {
	f := newFixture(t, NewPluginConfig())

	if err := f.plugin.onAdvertisement(&events.EventInfo{Event: "not an advertisement"}); err != nil {
		t.Error(err)
	}

	if len(f.plugin.devices) != 0 {
		t.Error("a device from nothing")
	}
}

func TestStop_DeregistersTheEntities(t *testing.T) {
	f := newFixture(t, NewPluginConfig())
	f.hear("s", goveeAddress, -61, goveeFrame)

	<-f.plugin.Stop()

	if !reflect.DeepEqual(f.container.deregistered, []string{"bluetooth_1"}) {
		t.Errorf("deregistered %v", f.container.deregistered)
	}
}

func TestTheTestDeviceIsListedWithoutAnySource(t *testing.T) {
	config := NewPluginConfig()
	config.EnableTestDevices = true
	f := newFixture(t, config)

	f.plugin.HandleHttpList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if len(f.container.listed) != 1 {
		t.Errorf("listed %d", len(f.container.listed))
	}
}

func TestRelativeTime(t *testing.T) {
	for ago, want := range map[time.Duration]string{
		time.Second: "< 30s", 45 * time.Second: "< 1m", 5 * time.Minute: "5m", 3 * time.Hour: "3h",
	} {
		if got := RelativeTime(time.Now().Add(-ago)); got != want {
			t.Errorf("%v ago: %q, want %q", ago, got, want)
		}
	}
}
