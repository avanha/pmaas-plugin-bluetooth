package bluetooth

import (
	"fmt"
	"reflect"
	"testing"
	"time"

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

func buttonConfig() (PluginConfig, *BeaconButton) {
	config := NewPluginConfig()
	button := config.AddBeaconButton("dd-88-00-00-3d-36", "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID)).
		Double(bluetooth.MatchIBeacon(doubleUUID)).
		Triple(bluetooth.MatchIBeacon(tripleUUID)).
		Long(bluetooth.MatchIBeacon(longUUID))

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

	if len(button.frames) != 4 || button.burst != 3*time.Second || len(config.buttons) != 1 {
		t.Errorf("frames %v, burst %v", button.frames, button.burst)
	}

	if got := button.GetButtonData(); got.Name != "Kitchen" || got.Presses != 0 {
		t.Errorf("before the plugin has started: %+v", got)
	}
}

func TestAddBeaconButton_MistakesPanic(t *testing.T) {
	for name, mistake := range map[string]func(){
		"not an address": func() { c := NewPluginConfig(); c.AddBeaconButton("nonsense", "x") },
		"the address twice": func() {
			c := NewPluginConfig()
			c.AddBeaconButton(buttonAddress, "x")
			c.AddBeaconButton("dd:88:00:00:3d:36", "y")
		},
		"a thermometer's address": func() {
			c := NewPluginConfig()
			c.AddThermometer(buttonAddress, "x")
			c.AddBeaconButton(buttonAddress, "y")
		},
		"a button's, as a thermometer": func() {
			c := NewPluginConfig()
			c.AddBeaconButton(buttonAddress, "y")
			c.AddThermometer(buttonAddress, "x")
		},
		"the same frame for two presses": func() {
			c := NewPluginConfig()
			c.AddBeaconButton(buttonAddress, "x").Single(bluetooth.MatchIBeacon(singleUUID)).Double(bluetooth.MatchIBeacon(singleUUID))
		},
		"a frame with no uuid": func() {
			c := NewPluginConfig()
			c.AddBeaconButton(buttonAddress, "x").Single(bluetooth.IBeaconMatch{})
		},
		"no burst":         func() { c := NewPluginConfig(); c.AddBeaconButton(buttonAddress, "x").Burst(0) },
		"a negative burst": func() { c := NewPluginConfig(); c.AddBeaconButton(buttonAddress, "x").Burst(-time.Second) },
		"no presses at all": func() {
			c := NewPluginConfig()
			c.AddBeaconButton(buttonAddress, "x")
			NewPlugin(c)
		},
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
	config.AddBeaconButton(buttonAddress, "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID).WithMinor(1)).
		Double(bluetooth.MatchIBeacon(singleUUID).WithMinor(2)).
		Long(bluetooth.MatchIBeacon(singleUUID))

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
	config.AddBeaconButton(buttonAddress, "Kitchen").Single(bluetooth.MatchIBeacon(singleUUID)).Burst(10 * time.Second)

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

func TestAddBeaconButton_TheMoreSpecificFrameWinsWhateverTheOrderOfPresses(t *testing.T) {
	config := NewPluginConfig()
	config.AddBeaconButton(buttonAddress, "Kitchen").
		Single(bluetooth.MatchIBeacon(singleUUID)).                        // any minor, any major
		Double(bluetooth.MatchIBeacon(singleUUID).WithMinor(2)).           // one minor
		Triple(bluetooth.MatchIBeacon(singleUUID).WithMajor(7)).           // one major
		Long(bluetooth.MatchIBeacon(singleUUID).WithMajor(7).WithMinor(3)) // both

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

func TestBeaconButton_WhichWhenTwoFramesAreEquallySpecificTheFirstKindOfPressWins(t *testing.T) {
	b := &BeaconButton{name: "x", frames: map[spidevice.ButtonPress]bluetooth.IBeaconMatch{
		spidevice.PressSingle: bluetooth.MatchIBeacon(singleUUID).WithMinor(1),
		spidevice.PressLong:   bluetooth.MatchIBeacon(singleUUID).WithMajor(1),
	}}

	for range 20 {
		if press, ok := b.which(bluetooth.IBeacon{UUID: singleUUID, Major: 1, Minor: 1}); !ok || press != spidevice.PressSingle {
			t.Fatalf("%v %v", press, ok)
		}
	}
}
