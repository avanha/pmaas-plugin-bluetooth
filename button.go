package bluetooth

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/avanha/pmaas-spi/bluetooth"
	spicommon "github.com/avanha/pmaas-spi/common"
	spidevice "github.com/avanha/pmaas-spi/device"
	environmental "github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
)

// BeaconButton is a button that is a Bluetooth beacon advertising an iBeacon frame for a few seconds after it is pressed, a
// different one for each way of pressing it. IBeaconButton makes one, you say which frame is which press, and Add it to the
// configuration; the plugin announces each press (see spidevice.ButtonPressedEvent). A device package may know more about its
// beacon, such as its battery (bluecharm.Button).
//
// Once the plugin has started it is a spidevice.IButton, so it goes wherever one does. Before then, and after the plugin stops,
// it answers as a button that has not been pressed.
type BeaconButton struct {
	*bluetooth.ButtonHandle
	address string
	name    string
	presses *bluetooth.IBeaconPresses
}

var _ bluetooth.ConfiguredDevice = (*BeaconButton)(nil)
var _ bluetooth.Addressed = (*BeaconButton)(nil)
var _ bluetooth.MakeAndModeler = (*BeaconButton)(nil)
var _ bluetooth.Validator = (*BeaconButton)(nil)
var _ bluetooth.ButtonBinder = (*BeaconButton)(nil)
var _ spidevice.IButton = (*BeaconButton)(nil)

// IBeaconButton is a beacon button by the beacon's address, which is any case with ":" or "-". Say which frame is which press on
// what it returns; at least one is needed. It panics if the address is not a Bluetooth address, like regexp.MustCompile does for a
// pattern that can't be: it is written in the program.
func IBeaconButton(address string, name string) *BeaconButton {
	normal, ok := bluetooth.NormalizeAddress(address)
	if !ok {
		panic(fmt.Sprintf("Button address %q is not a Bluetooth address like AA:BB:CC:DD:EE:FF", address))
	}

	return &BeaconButton{
		ButtonHandle: bluetooth.NewButtonHandle(name), address: normal, name: name, presses: bluetooth.NewIBeaconPresses(name),
	}
}

// Single is the frame the beacon advertises after a single click. Each press needs a frame of its own. It panics if another
// press has the same frame, like regexp.MustCompile does for a pattern that can't be.
func (b *BeaconButton) Single(frame bluetooth.IBeaconMatch) *BeaconButton {
	b.presses.Single(frame)
	return b
}

// Double is the frame the beacon advertises after a double click.
func (b *BeaconButton) Double(frame bluetooth.IBeaconMatch) *BeaconButton {
	b.presses.Double(frame)
	return b
}

// Triple is the frame the beacon advertises after a triple click.
func (b *BeaconButton) Triple(frame bluetooth.IBeaconMatch) *BeaconButton {
	b.presses.Triple(frame)
	return b
}

// Long is the frame the beacon advertises after a long press.
func (b *BeaconButton) Long(frame bluetooth.IBeaconMatch) *BeaconButton {
	b.presses.Long(frame)
	return b
}

// Burst is how long the beacon goes on advertising after a press. The default is three seconds. See
// bluetooth.IBeaconPresses.Burst.
func (b *BeaconButton) Burst(burst time.Duration) *BeaconButton { b.presses.Burst(burst); return b }

func (b *BeaconButton) Name() string            { return b.name }
func (b *BeaconButton) Kind() bluetooth.Kind    { return bluetooth.KindButton }
func (b *BeaconButton) Address() string         { return b.address }
func (b *BeaconButton) MakeAndModel() string    { return "iBeacon button" }
func (b *BeaconButton) NeedsScanResponse() bool { return false }
func (b *BeaconButton) Validate() error         { return b.presses.Validate() }
func (b *BeaconButton) New() bluetooth.Device {
	return &beaconButtonDevice{hearer: b.presses.NewHearer()}
}

func (b *BeaconButton) Claims(a *bluetooth.AdvertisementEvent) bool { return a.Address == b.address }

// Rules want every frame as it is heard: the first of a burst is the press, and nothing may wait.
func (b *BeaconButton) Rules() []bluetooth.Rule {
	return []bluetooth.Rule{{Name: "button " + b.name, Address: b.address, Latency: bluetooth.Immediate}}
}

type beaconButtonDevice struct {
	hearer *bluetooth.PressHearer
}

func (d *beaconButtonDevice) Hear(a *bluetooth.AdvertisementEvent, now time.Time) bluetooth.Result {
	return bluetooth.Result{Presses: d.hearer.Hear(a, now)}
}

// button is a beacon button that the plugin has started to watch. Owned by the plugin goroutine.
type button struct {
	config bluetooth.ConfiguredDevice
	name   string
	id     string
	data   spidevice.Button

	// impl is the device's own state, and disabled is that it panicked, so is not heard any more.
	impl     bluetooth.Device
	disabled bool

	// lastHeard is when anything was last heard from the beacon, and rssi how loudly. battery is what the device said of its battery,
	// if it has, and millivolts the voltage it gave.
	lastHeard  time.Time
	rssi       int
	battery    environmental.BatteryData
	millivolts int

	stub *buttonStub
}

var _ spidevice.IButton = (*button)(nil)

func (b *button) GetButtonData() spidevice.Button {
	return b.data
}

// buttonStub is the button as every other goroutine sees it: each call hops onto the plugin's.
type buttonStub struct {
	wrapper atomic.Pointer[spicommon.ThreadSafeEntityWrapper[spidevice.IButton]]
}

var _ spidevice.IButton = (*buttonStub)(nil)

func (s *buttonStub) GetButtonData() spidevice.Button {
	return spicommon.ThreadSafeEntityWrapperExecValueFunc(
		s.wrapper.Load(),
		func(target spidevice.IButton) spidevice.Button { return target.GetButtonData() })
}

// startButtons registers an entity for each button. Runs on the plugin goroutine.
func (p *plugin) startButtons() {
	for _, configured := range p.buttonDevices {
		address := configured.(bluetooth.Addressed).Address()

		b := &button{
			config: configured,
			name:   configured.Name(),
			data:   spidevice.Button{Name: configured.Name()},
			impl:   configured.New(),
			stub:   &buttonStub{},
		}

		b.stub.wrapper.Store(&spicommon.ThreadSafeEntityWrapper[spidevice.IButton]{Container: p.container, Entity: b})

		id, err := p.container.RegisterEntity(address, spidevice.IButtonType, b.name,
			func() (any, error) { return b.stub, nil })
		if err != nil {
			p.log.Error("button could not be registered", "name", b.name, "address", address, "error", err)

			continue
		}

		b.id = id
		p.buttons[address] = b

		if binder, ok := configured.(bluetooth.ButtonBinder); ok {
			event := spicommon.StubAvailableEvent[spidevice.IButton]{EntityEvent: b.entityEvent(), EntityStub: b.stub}
			if err := binder.Bind(p.container, id, event); err != nil {
				p.log.Error("unable to provide the button stub", "error", err)
			}
		}
	}
}

// stopButtons deregisters the buttons. Runs on the plugin goroutine.
func (p *plugin) stopButtons() {
	for address, b := range p.buttons {
		if binder, ok := b.config.(bluetooth.ButtonBinder); ok {
			binder.Unbind()
		}

		if err := p.container.DeregisterEntity(b.id); err != nil {
			p.log.Error("button could not be deregistered", "name", b.name, "error", err)
		}

		b.stub.wrapper.Store(nil)
		delete(p.buttons, address)
	}
}

func (b *button) entityEvent() events.EntityEvent {
	return events.EntityEvent{Id: b.id, EntityType: spidevice.IButtonType, Name: b.name}
}

// hearButton takes in an advertisement from a button's address, and announces the presses the device finds in it.
func (p *plugin) hearButton(b *button, a bluetooth.AdvertisementEvent) {
	now := p.now()
	b.lastHeard, b.rssi = now, a.RSSI

	result := p.hear(b.name, b.impl, &b.disabled, &a, now)

	if result.Battery != nil {
		b.battery = environmental.BatteryData{Level: *result.Battery, LastUpdateTime: now}
	}

	if result.Millivolts != nil {
		b.millivolts = *result.Millivolts
	}

	for _, press := range result.Presses {
		b.data.Presses++
		b.data.LastPress, b.data.LastPressTime = press, now

		p.log.Debug("button pressed", "name", b.name, "press", press, "source", a.Source, "rssi", a.RSSI, "behind", now.Sub(a.Time))

		p.broadcast(b.id, spidevice.ButtonPressedEvent{EntityEvent: b.entityEvent(), Press: press, Time: now, Count: b.data.Presses})
		p.broadcast(b.id, events.EntityStateChangedEvent{EntityEvent: b.entityEvent(), NewState: b.data})
	}
}

// slowHear is how long a device may take to hear an advertisement before it is warned about: it runs on the goroutine every
// advertisement goes through.
const slowHear = 20 * time.Millisecond

// hear gives the advertisement to a device, which is someone else's code. A panic disables the device, with a log, instead of
// taking the plugin's goroutine down, and a slow call is warned about. Runs on the plugin goroutine.
func (p *plugin) hear(label string, impl bluetooth.Device, disabled *bool, a *bluetooth.AdvertisementEvent, now time.Time) (result bluetooth.Result) {
	if impl == nil || *disabled {
		return bluetooth.Result{}
	}

	started := time.Now()

	defer func() {
		if r := recover(); r != nil {
			*disabled = true
			result = bluetooth.Result{}

			p.log.Error("a device panicked hearing an advertisement, so it is not heard any more", "device", label, "address", a.Address, "panic", r)

			return
		}

		if took := time.Since(started); took > slowHear {
			p.log.Warn("a device was slow to hear an advertisement", "device", label, "address", a.Address, "took", took)
		}
	}()

	return impl.Hear(a, now)
}

func (p *plugin) broadcast(id string, event any) {
	if err := p.container.BroadcastEvent(id, event); err != nil {
		p.log.Error("unable to broadcast", "type", fmt.Sprintf("%T", event), "error", err)
	}
}
