package bluetooth

import (
	"fmt"
	environmental "github.com/avanha/pmaas-spi/environment"
	"html/template"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/avanha/pmaas-bluetooth-devices/bluecharm"
	"github.com/avanha/pmaas-spi/bluetooth"
	"github.com/avanha/pmaas-spi/clock"
	spidevice "github.com/avanha/pmaas-spi/device"
	"github.com/avanha/pmaas-spi/events"
)

const (
	buttonAddress = "DD:88:00:00:3D:36"

	// The UUIDs a BlueCharm BC011 Pro advertises after a single, double and triple click, as captured.
	singleUUID = "426c7565-4368-6172-6d42-6561636f6e77"
	doubleUUID = "7777772e-6b6b-6d63-6e2e-636f6d000006"
	tripleUUID = "7777772e-6b6b-6d63-6e2e-636f6d000007"
	longUUID   = "7777772e-6b6b-6d63-6e2e-636f6d000004"
)

// ibeaconFrame is the advertising data of an iBeacon, as the proxy captured them from the beacon.
func ibeaconFrame(uuid string, major uint16, minor uint16) string {
	compact := ""
	for _, c := range uuid {
		if c != '-' {
			compact += string(c)
		}
	}

	return fmt.Sprintf("0201061aff4c000215%s%04x%04xc9", compact, major, minor)
}

// buttonConfig is a BlueCharm beacon, which gives its battery as well as its presses.
func buttonConfig() (PluginConfig, *bluecharm.BeaconButton) {
	config := NewPluginConfig()
	button := bluecharm.Button("dd-88-00-00-3d-36", "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID)).
		Double(bluetooth.MatchIBeacon(doubleUUID)).
		Triple(bluetooth.MatchIBeacon(tripleUUID)).
		Long(bluetooth.MatchIBeacon(longUUID))

	config.Add(button)

	return config, button
}

func (c *fakeContainer) buttonEvents() []spidevice.ButtonPressedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []spidevice.ButtonPressedEvent

	for _, b := range c.broadcasts {
		if e, ok := b.(spidevice.ButtonPressedEvent); ok {
			result = append(result, e)
		}
	}

	return result
}

func pressesOf(c *fakeContainer) []string {
	var result []string

	for _, e := range c.buttonEvents() {
		result = append(result, e.Press.String())
	}

	return result
}

func TestAddBeaconButton_IsConfiguredByAddressAndPress(t *testing.T) {
	config, button := buttonConfig()

	if button.Address() != buttonAddress || button.Name() != "Kitchen" || button.EntityId() != "" {
		t.Errorf("%s %s %q", button.Address(), button.Name(), button.EntityId())
	}

	if len(config.added) != 1 {
		t.Errorf("%d devices added", len(config.added))
	}

	if got := button.GetButtonData(); got.Name != "Kitchen" || got.Presses != 0 {
		t.Errorf("before the plugin has started: %+v", got)
	}
}

func TestAdd_AButtonThatIsBadlyConfiguredPanics(t *testing.T) {
	for name, mistake := range map[string]func(){
		"not an address": func() { IBeaconButton("nonsense", "x") },
		"the address twice": func() {
			c := NewPluginConfig()
			c.Add(IBeaconButton(buttonAddress, "x").Single(bluetooth.MatchIBeacon(singleUUID)))
			c.Add(IBeaconButton("dd:88:00:00:3d:36", "y").Single(bluetooth.MatchIBeacon(singleUUID)))
		},
		"a thermometer's address": func() {
			c := NewPluginConfig()
			c.AddThermometer(buttonAddress, "x")
			c.Add(IBeaconButton(buttonAddress, "y").Single(bluetooth.MatchIBeacon(singleUUID)))
		},
		"a button's, as a thermometer": func() {
			c := NewPluginConfig()
			c.Add(IBeaconButton(buttonAddress, "y").Single(bluetooth.MatchIBeacon(singleUUID)))
			c.AddThermometer(buttonAddress, "x")
		},
		"the same frame for two presses": func() {
			IBeaconButton(buttonAddress, "x").Single(bluetooth.MatchIBeacon(singleUUID)).Double(bluetooth.MatchIBeacon(singleUUID))
		},
		"a frame that says nothing": func() { IBeaconButton(buttonAddress, "x").Single(bluetooth.IBeaconMatch{}) },
		"no burst":                  func() { IBeaconButton(buttonAddress, "x").Burst(0) },
		"a negative burst":          func() { IBeaconButton(buttonAddress, "x").Burst(-time.Second) },
		"no presses at all":         func() { c := NewPluginConfig(); c.Add(IBeaconButton(buttonAddress, "x")) },
		"nothing":                   func() { c := NewPluginConfig(); c.Add(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()

			mistake()
		}()
	}
}

func TestAddBeaconButton_ThePressesOfOneUUIDAreToldApartByMajorAndMinor(t *testing.T) {
	config := NewPluginConfig()
	config.Add(IBeaconButton(buttonAddress, "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID).WithMinor(1)).
		Double(bluetooth.MatchIBeacon(singleUUID).WithMinor(2)).
		Long(bluetooth.MatchIBeacon(singleUUID)))

	f := newFixture(t, config)

	// Minor 2 is a double click, though the general frame (any minor) is the long press: the more specific wins.
	f.hear("s", buttonAddress, -60, ibeaconFrame(singleUUID, 1, 2))
	f.advance(10 * time.Second)
	f.hear("s", buttonAddress, -60, ibeaconFrame(singleUUID, 1, 1))
	f.advance(10 * time.Second)
	f.hear("s", buttonAddress, -60, ibeaconFrame(singleUUID, 1, 9))

	if got := pressesOf(f.container); !reflect.DeepEqual(got, []string{"double", "single", "long"}) {
		t.Errorf("presses %v", got)
	}
}

func TestStart_RegistersTheButtonAsAnEntityAndAsksTheSourcesForEveryFrameOfIt(t *testing.T) {
	config, button := buttonConfig()
	f := newFixture(t, config)

	if f.container.entities[buttonAddress] != spidevice.IButtonType || button.EntityId() != "bluetooth_1" {
		t.Errorf("entities %v, id %q", f.container.entities, button.EntityId())
	}

	var rule *bluetooth.Rule

	for _, r := range f.container.interests()[0].Rules {
		if r.Name == "button Kitchen" {
			rule = &r
		}
	}

	if rule == nil || rule.Address != buttonAddress || rule.Latency != bluetooth.Immediate {
		t.Fatalf("rule %+v in %+v", rule, f.container.interests()[0].Rules)
	}

	if err := bluetooth.PublishInterest(f.container, "x", f.container.interests()[0].Rules, false); err != nil {
		t.Errorf("what is wanted is not valid: %v", err)
	}
}

func TestButton_APressIsAnnouncedAtOnceByTheFirstFrameOfIt(t *testing.T) {
	config, button := buttonConfig()
	f := newFixture(t, config)

	f.advance(5 * time.Second)
	f.hear("s", buttonAddress, -95, ibeaconFrame(doubleUUID, 1, 1))

	events := f.container.buttonEvents()
	if len(events) != 1 {
		t.Fatalf("%d events", len(events))
	}

	e := events[0]
	if e.Press != spidevice.PressDouble || e.Count != 1 || !e.Time.Equal(f.clock) || e.Id != "bluetooth_1" || e.EntityType != spidevice.IButtonType || e.Name != "Kitchen" {
		t.Errorf("event %+v", e)
	}

	// And the entity's state changed, for anything that listens to that.
	var state spidevice.Button

	for _, s := range f.container.states() {
		if b, ok := s.NewState.(spidevice.Button); ok {
			state = b
		}
	}

	if state.Presses != 1 || state.LastPress != spidevice.PressDouble || !state.LastPressTime.Equal(f.clock) {
		t.Errorf("state %+v", state)
	}

	// The handle the assembly holds answers with it.
	if got := button.GetButtonData(); got.Presses != 1 || got.LastPress != spidevice.PressDouble || got.Name != "Kitchen" {
		t.Errorf("the handle says %+v", got)
	}
}

func TestButton_TheRestOfABurstIsNotAnotherPress(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	// Frames at 100 ms for the three seconds the beacon advertises, heard by two sources, some lost.
	for i := 0; i < 30; i++ {
		f.hear("a", buttonAddress, -95, ibeaconFrame(singleUUID, 3838, 4949))

		if i%3 == 0 {
			f.hear("b", buttonAddress, -90, ibeaconFrame(singleUUID, 3838, 4949))
		}

		f.advance(100 * time.Millisecond)
	}

	if got := pressesOf(f.container); !reflect.DeepEqual(got, []string{"single"}) {
		t.Errorf("presses %v", got)
	}
}

func TestButton_AnotherPressAfterTheBurstHasEndedIsAnotherPress(t *testing.T) {
	config, button := buttonConfig()
	f := newFixture(t, config)

	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	// Only the first and the last frame of a three second burst were heard: the same press.
	f.advance(3 * time.Second)
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	if len(pressesOf(f.container)) != 1 {
		t.Fatalf("presses %v", pressesOf(f.container))
	}

	// A quiet spell longer than the burst, and then it is heard again.
	f.advance(3600 * time.Millisecond)
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	events := f.container.buttonEvents()
	if len(events) != 2 || events[1].Count != 2 {
		t.Fatalf("events %+v", events)
	}

	if button.GetButtonData().Presses != 2 {
		t.Errorf("%+v", button.GetButtonData())
	}
}

func TestButton_EachKindOfPressIsItsOwn(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))
	f.advance(300 * time.Millisecond)
	f.hear("s", buttonAddress, -90, ibeaconFrame(doubleUUID, 1, 1))
	f.advance(300 * time.Millisecond)
	f.hear("s", buttonAddress, -90, ibeaconFrame(tripleUUID, 1, 1))
	f.advance(300 * time.Millisecond)
	f.hear("s", buttonAddress, -90, ibeaconFrame(longUUID, 1, 1))
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1)) // the rest of the first

	if got := pressesOf(f.container); !reflect.DeepEqual(got, []string{"single", "double", "triple", "long"}) {
		t.Errorf("presses %v", got)
	}
}

func TestButton_TheBurstCanBeSetToMatchTheBeacon(t *testing.T) {
	config := NewPluginConfig()
	config.Add(IBeaconButton(buttonAddress, "Kitchen").Single(bluetooth.MatchIBeacon(singleUUID)).Burst(10 * time.Second))

	f := newFixture(t, config)

	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))
	f.advance(8 * time.Second)
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))
	f.advance(3 * time.Second) // 11 seconds since the first, 3 since the last: still the same burst
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	if len(pressesOf(f.container)) != 1 {
		t.Errorf("presses %v", pressesOf(f.container))
	}

	f.advance(11 * time.Second)
	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	if len(pressesOf(f.container)) != 2 {
		t.Errorf("presses %v", pressesOf(f.container))
	}
}

func TestButton_OnlyTheButtonsOwnFramesFromItsAddressArePresses(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	// The same frame from another address is somebody else's beacon.
	f.hear("s", "DD:88:00:00:3D:37", -60, ibeaconFrame(singleUUID, 1, 1))

	// Frames from the button's address that are not any press: an iBeacon of another UUID, one that is not an iBeacon (the
	// beacon's own status frame), and nothing at all.
	f.hear("s", buttonAddress, -60, ibeaconFrame("00000000-0000-0000-0000-000000000000", 1, 1))
	f.hear("s", buttonAddress, -60, "0201060303aafe0e16aafe220b67dd8800003d36063e")
	f.container.deliver(bluetooth.AdvertisementEvent{Source: "s", Address: buttonAddress, Time: f.clock})

	if got := pressesOf(f.container); len(got) != 0 {
		t.Errorf("presses %v", got)
	}
}

func TestButton_TheBeaconAlsoAppearsInTheListOfDevicesByName(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	f.container.deliver(bluetooth.SightingEvent{Source: "s", Address: buttonAddress, AddressType: bluetooth.AddressRandom, RSSI: -85, Time: f.clock, LocalName: "BCPro_222616"})

	if d := f.device(buttonAddress); d.ReportedName != "BCPro_222616" || d.RSSIData.RSSI != -85 {
		t.Errorf("device %+v", d)
	}
}

func TestStop_DeregistersTheButtonAndTheHandleGoesBackToNeverPressed(t *testing.T) {
	config, button := buttonConfig()
	f := newFixture(t, config)

	f.hear("s", buttonAddress, -90, ibeaconFrame(singleUUID, 1, 1))

	if button.GetButtonData().Presses != 1 {
		t.Fatal("not pressed")
	}

	<-f.plugin.Stop()

	if !reflect.DeepEqual(f.container.deregistered, []string{"bluetooth_1"}) {
		t.Errorf("deregistered %v", f.container.deregistered)
	}

	if got := button.GetButtonData(); got.Presses != 0 || got.Name != "Kitchen" || button.EntityId() != "" {
		t.Errorf("%+v %q", got, button.EntityId())
	}

	// Frames heard after it stopped are nothing.
	f.hear("s", buttonAddress, -90, ibeaconFrame(doubleUUID, 1, 1))

	if len(pressesOf(f.container)) != 1 {
		t.Errorf("a press after stopping: %v", pressesOf(f.container))
	}
}

func TestButton_ARunningPluginKeepsItsButtonsAcrossTheMinute(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	f.container.deliver(clock.MinuteChangedEvent{Time: f.clock})

	rules := f.container.interests()
	last := rules[len(rules)-1].Rules

	found := false

	for _, r := range last {
		found = found || r.Name == "button Kitchen"
	}

	if !found {
		t.Error("the button is not asked for on the minute")
	}

	_ = events.EventInfo{}
}

func TestAddBeaconButton_TheSlotsCanBeToldApartByMajorAlone(t *testing.T) {
	// As the beacon is configured: each press in the slot of that number, with a major to match, and the UUID not mattering.
	config := NewPluginConfig()
	config.Add(IBeaconButton(buttonAddress, "Couch").
		Single(bluetooth.MatchAnyIBeacon().WithMajor(0)).
		Double(bluetooth.MatchAnyIBeacon().WithMajor(1)).
		Triple(bluetooth.MatchAnyIBeacon().WithMajor(2)).
		Long(bluetooth.MatchAnyIBeacon().WithMajor(3)))

	f := newFixture(t, config)

	for _, frame := range []string{
		ibeaconFrame(singleUUID, 0, 1), ibeaconFrame(doubleUUID, 1, 1), ibeaconFrame(tripleUUID, 2, 1), ibeaconFrame(longUUID, 3, 1),
		ibeaconFrame(singleUUID, 4, 1), // a slot that is not a press
	} {
		f.advance(10 * time.Second)
		f.hear("s", buttonAddress, -90, frame)
	}

	if got := pressesOf(f.container); !reflect.DeepEqual(got, []string{"single", "double", "triple", "long"}) {
		t.Errorf("presses %v", got)
	}
}

func TestAddBeaconButton_TheMoreSpecificFrameWinsWhateverTheOrderOfPresses(t *testing.T) {
	config := NewPluginConfig()
	config.Add(IBeaconButton(buttonAddress, "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID)).                         // any minor, any major
		Double(bluetooth.MatchIBeacon(singleUUID).WithMinor(2)).            // one minor
		Triple(bluetooth.MatchIBeacon(singleUUID).WithMajor(7)).            // one major
		Long(bluetooth.MatchIBeacon(singleUUID).WithMajor(7).WithMinor(3))) // both

	f := newFixture(t, config)

	frames := []struct {
		major, minor uint16
		want         string
	}{{1, 1, "single"}, {1, 2, "double"}, {7, 1, "triple"}, {7, 3, "long"}}

	var got []string

	for _, frame := range frames {
		f.advance(10 * time.Second)
		f.hear("s", buttonAddress, -60, ibeaconFrame(singleUUID, frame.major, frame.minor))
	}

	for _, e := range f.container.buttonEvents() {
		got = append(got, e.Press.String())
	}

	want := []string{"single", "double", "triple", "long"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("presses %v, want %v", got, want)
	}
}

// tlmFrame is the advertising data of an Eddystone-TLM frame with the battery at the given voltage.
func tlmFrame(millivolts uint16) string {
	return fmt.Sprintf("0201060303aafe1116aafe2000%04x1880000001000000ffff", millivolts)
}

func TestBeacon_IsOnThePageWithWhatWasHeardAndItsBattery(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	items := func() []publishedBeacon {
		var beacons []publishedBeacon

		for _, item := range f.plugin.listItems() {
			if b, ok := item.(publishedBeacon); ok {
				beacons = append(beacons, b)
			}
		}

		return beacons
	}

	// Before anything is heard it is there, saying so.
	beacons := items()
	if len(beacons) != 1 || beacons[0].Name != "Kitchen" || beacons[0].Address != buttonAddress || beacons[0].Heard ||
		!beacons[0].Battery.IsEmpty() || beacons[0].LastPress != "" {
		t.Fatalf("before: %+v", beacons)
	}

	f.advance(time.Minute)
	f.hear("s", buttonAddress, -71, tlmFrame(2800))

	b := items()[0]
	if !b.Heard || !b.LastHeard.Equal(f.clock) || b.RSSI.RSSI != -71 || b.Battery.Level != 55 || b.Millivolts != 2800 || b.LastPress != "" {
		t.Errorf("after a telemetry frame: %+v", b)
	}

	// A telemetry frame that doesn't give a voltage (a beacon with no battery gauge says 0) is not a flat battery.
	f.hear("s", buttonAddress, -71, tlmFrame(0))

	if b := items()[0]; b.Battery.Level != 55 || b.Millivolts != 2800 {
		t.Errorf("after a frame with no voltage: %+v", b)
	}

	// A press is heard too, and doesn't forget the battery.
	f.advance(time.Minute)
	f.hear("s", buttonAddress, -80, ibeaconFrame(doubleUUID, 1, 1))

	b = items()[0]
	if b.RSSI.RSSI != -80 || b.Battery.Level != 55 || b.LastPress != "double" || b.Presses != 1 {
		t.Errorf("after a press: %+v", b)
	}

	if len(f.container.buttonEvents()) != 1 {
		t.Errorf("the telemetry frame was taken for a press: %v", pressesOf(f.container))
	}
}

func TestBeaconTemplate_ShowsTheBatteryOrSaysItIsUnknown(t *testing.T) {
	tmpl, err := template.New("").Funcs(template.FuncMap(beaconTemplate.FuncMap)).ParseFS(contentFS, "content/templates/bluetooth_beacon.htmlt")
	if err != nil {
		t.Fatal(err)
	}

	render := func(b publishedBeacon) string {
		var out strings.Builder
		if err := tmpl.Lookup("bluetooth_beacon.htmlt").Execute(&out, b); err != nil {
			t.Fatal(err)
		}

		return out.String()
	}

	now := time.Now()
	known := render(publishedBeacon{Name: "Kitchen", Kind: "Beacon button", Heard: true, LastHeard: now,
		RSSI: environmental.RSSIData{RSSI: -70, LastUpdateTime: now}, Battery: environmental.BatteryData{Level: 55, LastUpdateTime: now}, Millivolts: 2800,
		LastPress: "double", LastPressTime: now, Presses: 3})

	for _, want := range []string{"Kitchen", "Beacon button", `<span class="value">55</span>`, "2800 mV", "-70", "bi-battery-half", "Last press: double", "3 in all"} {
		if !strings.Contains(known, want) {
			t.Errorf("missing %q:\n%s", want, known)
		}
	}

	if strings.Contains(known, "battery unknown") {
		t.Error("said the battery is unknown")
	}

	unknown := render(publishedBeacon{Name: "Kitchen", Kind: "Beacon button"})
	for _, want := range []string{"battery unknown", "not yet", "Not pressed yet"} {
		if !strings.Contains(unknown, want) {
			t.Errorf("missing %q:\n%s", want, unknown)
		}
	}

	if strings.Contains(unknown, `class="battery-data"`) || strings.Contains(unknown, `class="rssi-data"`) {
		t.Errorf("showed a battery or signal it hasn't got:\n%s", unknown)
	}
}

func TestBatteryIcon_LooksLikeWhatIsLeft(t *testing.T) {
	for level, want := range map[int]string{100: "battery-full", 70: "battery-full", 69: "battery-half", 30: "battery-half", 29: "battery", 0: "battery"} {
		if got := batteryIcon(level); got != want {
			t.Errorf("%d%% is %s, want %s", level, got, want)
		}
	}

	if !isLowBattery(19) || isLowBattery(20) {
		t.Error("low battery threshold")
	}
}

func TestThermometerTemplate_ShowsTheReadingAndTheBatteryIcon(t *testing.T) {
	tmpl, err := template.New("").Funcs(template.FuncMap(thermometerTemplate.FuncMap)).ParseFS(contentFS, "content/templates/bluetooth_thermometer.htmlt")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	var out strings.Builder
	reading := publishedWirelessThermometer{environmental.WirelessThermometer{
		Name:        "Basement",
		RSSIData:    environmental.RSSIData{RSSI: -60, LastUpdateTime: now},
		BatteryData: environmental.BatteryData{Level: 15, LastUpdateTime: now},
		SensorData:  environmental.SensorData{HasData: true, Temperature: 20, HasHumidity: true, Humidity: 45, LastUpdateTime: now},
	}}

	if err := tmpl.Lookup("bluetooth_thermometer.htmlt").Execute(&out, reading); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Basement", "20.00 C", "68.00 F", "45.0%", `bi-battery low`, `<span class="value">15</span>`, "-60"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestThermometerTemplate_ShowsTwoDecimalsOfTemperatureAndOneOfHumidityWhateverTheReadingHas(t *testing.T) {
	tmpl, err := template.New("").Funcs(template.FuncMap(thermometerTemplate.FuncMap)).ParseFS(contentFS, "content/templates/bluetooth_thermometer.htmlt")
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	reading := publishedWirelessThermometer{environmental.WirelessThermometer{
		Name:       "Basement",
		SensorData: environmental.SensorData{HasData: true, Temperature: 21.123456, HasHumidity: true, Humidity: 36.987654, LastUpdateTime: time.Now()},
	}}

	if err := tmpl.Lookup("bluetooth_thermometer.htmlt").Execute(&out, reading); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"21.12 C", "70.02 F", "37.0%"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}

	if strings.Contains(out.String(), "21.123") || strings.Contains(out.String(), "36.98") {
		t.Errorf("too many decimal places:\n%s", out.String())
	}
}

func TestListItems_ABeaconButtonIsListedOnceEvenWhenItAdvertisesAName(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	// Heard with a name, as it is once it advertises more than the triggers.
	f.hear("s", buttonAddress, -70, "0201060909"+"4b4b4d2d4b455931")

	beacons, devices := 0, 0

	for _, item := range f.plugin.listItems() {
		switch item.(type) {
		case publishedBeacon:
			beacons++
		case publishedDevice:
			devices++
		}
	}

	if beacons != 1 || devices != 0 {
		t.Errorf("%d beacons and %d devices", beacons, devices)
	}
}

// systemFrame is the advertising data of the BlueCharm BC011 Pro's System frame as a proxy captured it, with the battery at the given
// percentage, and its name in the scan response.
func systemFrame(percent byte) string {
	return fmt.Sprintf("0201060303aafe0e16aafe220b%02xdd8800003d36063e0d09424350726f5f323232363136", percent)
}

func TestBeacon_BatteryFromTheSystemFrameIsTheBeaconsOwnFigure(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	beacon := func() publishedBeacon { return f.plugin.listItems()[0].(publishedBeacon) }

	f.advance(time.Minute)
	f.hear("s", buttonAddress, -75, systemFrame(0x67)) // 103%: over 100 on a good cell

	b := beacon()
	if b.Battery.Level != 100 || b.Battery.LastUpdateTime != f.clock || b.Millivolts != 0 || !b.Heard {
		t.Errorf("103%% reported: %+v", b)
	}

	f.hear("s", buttonAddress, -75, systemFrame(62))

	if b := beacon(); b.Battery.Level != 62 {
		t.Errorf("62%% reported: %+v", b)
	}

	// It isn't a press, and its name isn't a second device on the page.
	if len(f.container.buttonEvents()) != 0 {
		t.Errorf("a System frame was taken for a press: %v", pressesOf(f.container))
	}

	for _, item := range f.plugin.listItems() {
		if _, isDevice := item.(publishedDevice); isDevice {
			t.Errorf("the beacon is listed as a device too")
		}
	}
}

func TestBeacon_AVoltageDoesNotReplaceTheBeaconsOwnLevelButIsKept(t *testing.T) {
	config, _ := buttonConfig()
	f := newFixture(t, config)

	f.hear("s", buttonAddress, -75, tlmFrame(2800)) // 55% by the curve

	if b := f.plugin.listItems()[0].(publishedBeacon); b.Battery.Level != 55 || b.Millivolts != 2800 {
		t.Fatalf("from the voltage: %+v", b)
	}

	f.hear("s", buttonAddress, -75, systemFrame(80))
	f.hear("s", buttonAddress, -75, tlmFrame(2700))

	if b := f.plugin.listItems()[0].(publishedBeacon); b.Battery.Level != 80 || b.Millivolts != 2700 {
		t.Errorf("after the beacon gave its own level: %+v", b)
	}
}
