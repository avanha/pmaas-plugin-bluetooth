package bluetooth

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/avanha/pmaas-bluetooth-devices/govee"
	"github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
	environmental "github.com/avanha/pmaas-spi/environment"
)

// fakeModel recognises a device by the name it advertises, and reads the temperature from the first byte of its manufacturer data
// under company 0xFFFF. hear can be replaced to do something else.
type fakeModel struct {
	hear func(a *bluetooth.AdvertisementEvent) bluetooth.Result
}

func (m *fakeModel) Name() string            { return "Fake F1" }
func (m *fakeModel) Kind() bluetooth.Kind    { return bluetooth.KindThermometer }
func (m *fakeModel) NeedsScanResponse() bool { return false }
func (m *fakeModel) New() bluetooth.Device   { return fakeDevice{m} }
func (m *fakeModel) Identify(known bluetooth.Fields) bool {
	return known.LocalName == "fake"
}
func (m *fakeModel) Rules() []bluetooth.Rule {
	return []bluetooth.Rule{{Name: "fake-f1", NamePrefix: "fake"}}
}

type fakeDevice struct{ m *fakeModel }

func (d fakeDevice) Hear(a *bluetooth.AdvertisementEvent, _ time.Time) bluetooth.Result {
	if d.m.hear != nil {
		return d.m.hear(a)
	}

	return bluetooth.Result{Reading: &bluetooth.Environment{Temperature: float32(a.ManufacturerData[0xffff][0])}}
}

// "fake" advertised as a name, with one byte of manufacturer data for company 0xFFFF.
const fakeFrame = "0509" + "66616b65" + "04ff" + "ffff" + "15"

func fixtureWith(t *testing.T, config PluginConfig) (*fixture, *bytes.Buffer) {
	t.Helper()

	f := &fixture{t: t, container: newFakeContainer(), clock: epoch}
	f.plugin = NewPlugin(config).(*plugin)
	f.plugin.now = func() time.Time { return f.clock }
	f.plugin.Init(f.container)

	logged := &bytes.Buffer{}
	f.plugin.log = slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.plugin.Start()

	return f, logged
}

func TestRecognize_OnlyTheModelsGivenAreRecognisedAndTheSourcesAreAskedForThem(t *testing.T) {
	config := NewPluginConfig()
	f, _ := fixtureWith(t, config)

	f.hear("s", goveeAddress, -61, goveeFrame)

	if dev := f.device(goveeAddress); dev.DeviceType != common.Unknown {
		t.Errorf("a Govee was recognised though no model was given: %+v", dev)
	}

	if rules := f.container.interests()[0].Rules; len(rules) != 0 {
		t.Errorf("rules %+v", rules)
	}
}

func TestRecognize_AModelGivenTwiceCountsOnce(t *testing.T) {
	config := NewPluginConfig()
	config.Recognize(govee.All()...)
	config.Recognize(govee.All()...)

	if len(config.models) != 1 {
		t.Errorf("%d models", len(config.models))
	}
}

func TestModel_AThirdPartyModelIsRecognisedAndReported(t *testing.T) {
	config := NewPluginConfig()
	config.Recognize(&fakeModel{})
	f, _ := fixtureWith(t, config)

	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)

	dev := f.device("10:20:30:40:50:60")
	if dev.DeviceMakeAndModel != "Fake F1" || dev.DeviceType != common.ThermometerAndHygrometer || dev.SensorData.Temperature != 0x15 ||
		f.container.entities["10:20:30:40:50:60"] != IWirelessThermometerType {
		t.Errorf("%+v", dev)
	}
}

func TestModel_APanicDisablesThatDeviceAndIsLoggedAndTheNextIsStillHeard(t *testing.T) {
	calls := 0
	model := &fakeModel{hear: func(a *bluetooth.AdvertisementEvent) bluetooth.Result {
		calls++

		if a.Address == "10:20:30:40:50:60" {
			panic("bad frame")
		}

		return bluetooth.Result{Battery: bluetooth.Percent(50)}
	}}

	config := NewPluginConfig()
	config.Recognize(model)
	f, logged := fixtureWith(t, config)

	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)
	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)
	f.hear("s", "10:20:30:40:50:61", -70, fakeFrame)

	if calls != 2 {
		t.Errorf("the device that panicked was heard again: %d calls", calls)
	}

	if !strings.Contains(logged.String(), "panicked") || !strings.Contains(logged.String(), "bad frame") {
		t.Errorf("not logged: %s", logged)
	}

	if f.device("10:20:30:40:50:61").BatteryData.Level != 50 {
		t.Error("the other device was not heard")
	}
}

func TestModel_ASlowDeviceIsWarnedAbout(t *testing.T) {
	model := &fakeModel{hear: func(*bluetooth.AdvertisementEvent) bluetooth.Result {
		time.Sleep(2 * slowHear)

		return bluetooth.Result{}
	}}

	config := NewPluginConfig()
	config.Recognize(model)
	f, logged := fixtureWith(t, config)

	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)

	if !strings.Contains(logged.String(), "slow") {
		t.Errorf("not warned: %s", logged)
	}
}

// fakeClaimer claims the advertisements that carry a name, whatever the address: like a phone, it is not known by one.
type fakeClaimer struct {
	claimed []string
	panics  bool
}

func (c *fakeClaimer) Name() string            { return "Tracker" }
func (c *fakeClaimer) Kind() bluetooth.Kind    { return bluetooth.KindThermometer }
func (c *fakeClaimer) NeedsScanResponse() bool { return false }
func (c *fakeClaimer) New() bluetooth.Device   { return fakeDevice{&fakeModel{}} }
func (c *fakeClaimer) Rules() []bluetooth.Rule {
	return []bluetooth.Rule{{Name: "tracker", NamePrefix: "fake"}}
}

func (c *fakeClaimer) Claims(a *bluetooth.AdvertisementEvent) bool {
	if c.panics {
		panic("claims")
	}

	c.claimed = append(c.claimed, a.Address)

	return a.LocalName == "fake"
}

func TestConfiguredDevice_OneThatClaimsByMoreThanAnAddressIsAskedOncePerAddress(t *testing.T) {
	claimer := &fakeClaimer{}
	config := NewPluginConfig()
	config.Add(claimer)
	f, _ := fixtureWith(t, config)

	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)
	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)
	f.hear("s", "10:20:30:40:50:61", -70, "020106")

	if len(claimer.claimed) != 2 {
		t.Errorf("asked %v", claimer.claimed)
	}

	dev := f.device("10:20:30:40:50:60")
	if dev.LocalName != "Tracker" || dev.SensorData.Temperature != 0x15 || f.container.entities["10:20:30:40:50:60"] != IWirelessThermometerType {
		t.Errorf("%+v", dev)
	}

	if f.device("10:20:30:40:50:61").impl != nil {
		t.Error("an advertisement that was not its own was claimed")
	}

	// Its rules are asked for each time the interest is published, since they may grow.
	var found bool

	for _, r := range f.container.interests()[0].Rules {
		found = found || r.Name == "tracker"
	}

	if !found {
		t.Error("its rules were not published")
	}
}

func TestConfiguredDevice_APanicWhileClaimingIsNoClaim(t *testing.T) {
	config := NewPluginConfig()
	config.Add(&fakeClaimer{panics: true})
	f, logged := fixtureWith(t, config)

	f.hear("s", "10:20:30:40:50:60", -70, fakeFrame)

	if f.device("10:20:30:40:50:60").impl != nil || !strings.Contains(logged.String(), "panicked") {
		t.Errorf("%s", logged)
	}
}

func TestConfiguredDevice_AnAddressedThermometerIsAnEntityFromTheStartAndAsksForScanResponsesIfItNeedsThem(t *testing.T) {
	config := NewPluginConfig()
	config.Add(govee.Thermometer("a4-c1-38-cc-e2-f0", "Office"))
	f, _ := fixtureWith(t, config)

	dev := f.device(goveeAddress)
	if dev.LocalName != "Office" || dev.DeviceMakeAndModel != "Govee GVH5075" || f.container.entities[goveeAddress] != IWirelessThermometerType {
		t.Errorf("%+v", dev)
	}

	f.hear("s", goveeAddress, -60, goveeFrame)

	if got := f.device(goveeAddress).SensorData; got.Temperature != 21.1 || !got.HasHumidity {
		t.Errorf("%+v", got)
	}

	var _ environmental.SensorData = dev.SensorData
}

type buttonWithoutAddress struct{ fakeClaimer }

func (buttonWithoutAddress) Kind() bluetooth.Kind { return bluetooth.KindButton }

func TestAdd_AButtonHasToHaveAFixedAddress(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()

	config := NewPluginConfig()
	config.Add(&buttonWithoutAddress{})
}

func TestAddThermometer_TheHandleReadsWhatTheDeviceReportsAndIsEmptyBeforeAndAfter(t *testing.T) {
	config := NewPluginConfig()
	handle := config.AddThermometer(goveeAddress, "Master Bedroom")

	if handle.Name() != "Master Bedroom" || handle.GetWirelessThermometerData().SensorData.HasData {
		t.Fatalf("before the plugin starts: %+v", handle.GetWirelessThermometerData())
	}

	f := newFixture(t, config)

	if got := handle.GetWirelessThermometerData(); got.Name != "Master Bedroom" || got.SensorData.HasData {
		t.Errorf("before anything is heard: %+v", got)
	}

	f.hear("s", goveeAddress, -61, goveeFrame)

	got := handle.GetWirelessThermometerData()
	if got.Name != "Master Bedroom" || !got.SensorData.HasData || got.SensorData.Temperature != 21.1 || got.BatteryData.Level != 100 || !got.SensorData.LastUpdateTime.Equal(f.clock) {
		t.Errorf("%+v", got)
	}

	f.plugin.Stop()

	if handle.GetWirelessThermometerData().SensorData.HasData {
		t.Error("the handle still reads after the plugin stopped")
	}
}
