package bluetooth

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/avanha/pmaas-spi/bluetooth"
	spicommon "github.com/avanha/pmaas-spi/common"
	spidevice "github.com/avanha/pmaas-spi/device"
	"github.com/avanha/pmaas-spi/events"
)

// defaultBurst is how long a beacon is assumed to go on advertising after a press: the BlueCharm's "Trigger Adv Duration"
// that most people set is a few seconds.
const defaultBurst = 3 * time.Second

// burstMargin is how much longer than the burst a quiet spell has to be for the next frame to be a new press and not the
// rest of the last.
const burstMargin = 500 * time.Millisecond

// presses are all the ways a button can be pressed, in the order they are looked for.
var presses = []spidevice.ButtonPress{spidevice.PressSingle, spidevice.PressDouble, spidevice.PressTriple, spidevice.PressLong}

// BeaconButton is a button that is a Bluetooth beacon, such as a BlueCharm BC011 Pro with its triggers set: it advertises a
// frame for a few seconds after it is pressed, a different one for each way of pressing it. Add one to the configuration with
// AddBeaconButton, say which frame is which press, and the plugin announces each press (see spidevice.ButtonPressedEvent).
//
// Once the plugin has started it is a spidevice.IButton, so it goes wherever one does. Before then, and after the plugin stops,
// it answers as a button that has not been pressed.
type BeaconButton struct {
	address string
	name    string
	burst   time.Duration
	frames  map[spidevice.ButtonPress]bluetooth.IBeaconMatch

	stub     spicommon.StubHolder[spidevice.IButton]
	entityId atomic.Pointer[string]
}

var _ spidevice.IButton = (*BeaconButton)(nil)

// Name is the button's name.
func (b *BeaconButton) Name() string {
	return b.name
}

// Address is the beacon's address.
func (b *BeaconButton) Address() string {
	return b.address
}

// EntityId is the ID the button is registered under once the plugin has started, which is what the events about it say it is
// from, or empty before then.
func (b *BeaconButton) EntityId() string {
	if id := b.entityId.Load(); id != nil {
		return *id
	}

	return ""
}

func (b *BeaconButton) GetButtonData() spidevice.Button {
	stub, err := b.stub.Stub()
	if err != nil {
		return spidevice.Button{Name: b.name}
	}

	return stub.GetButtonData()
}

// Single is the frame the beacon advertises after a single click. Each press needs a frame of its own. It panics if another
// press has the same frame, like regexp.MustCompile does for a pattern that can't be.
func (b *BeaconButton) Single(frame bluetooth.IBeaconMatch) *BeaconButton {
	return b.press(spidevice.PressSingle, frame)
}

// Double is the frame the beacon advertises after a double click.
func (b *BeaconButton) Double(frame bluetooth.IBeaconMatch) *BeaconButton {
	return b.press(spidevice.PressDouble, frame)
}

// Triple is the frame the beacon advertises after a triple click.
func (b *BeaconButton) Triple(frame bluetooth.IBeaconMatch) *BeaconButton {
	return b.press(spidevice.PressTriple, frame)
}

// Long is the frame the beacon advertises after a long press.
func (b *BeaconButton) Long(frame bluetooth.IBeaconMatch) *BeaconButton {
	return b.press(spidevice.PressLong, frame)
}

// Burst is how long the beacon goes on advertising after a press (the BlueCharm's "Trigger Adv Duration"). The default is three
// seconds. Frames of the same press that come sooner than that, and a little longer, are the rest of the press that was
// already announced, not another press, so two presses of the same kind closer together than this are one. It panics if it is not
// positive.
func (b *BeaconButton) Burst(burst time.Duration) *BeaconButton {
	if burst <= 0 {
		panic(fmt.Sprintf("bluetooth: button %q: the burst has to be longer than nothing, not %v", b.name, burst))
	}

	b.burst = burst

	return b
}

func (b *BeaconButton) press(press spidevice.ButtonPress, frame bluetooth.IBeaconMatch) *BeaconButton {
	if frame.IsEmpty() {
		panic(fmt.Sprintf("bluetooth: button %q: the %s press needs a frame that says something: use bluetooth.MatchIBeacon or MatchAnyIBeacon with WithMajor", b.name, press))
	}

	for other, existing := range b.frames {
		if other != press && sameFrame(existing, frame) {
			panic(fmt.Sprintf("bluetooth: button %q: the %s and %s presses can't both be %s", b.name, other, press, frame))
		}
	}

	b.frames[press] = frame

	return b
}

// sameFrame is whether two matches say the same thing.
func sameFrame(a bluetooth.IBeaconMatch, b bluetooth.IBeaconMatch) bool {
	return a.String() == b.String()
}

// which is the press a frame says, if it says one. A press whose match is more specific is looked for first, so that a UUID
// shared by two presses that differ in the minor is told apart.
func (b *BeaconButton) which(frame bluetooth.IBeacon) (spidevice.ButtonPress, bool) {
	var found spidevice.ButtonPress
	specificity := -1

	for _, press := range presses {
		match, ok := b.frames[press]
		if !ok || !match.Matches(frame) {
			continue
		}

		if score := match.Specificity(); score > specificity {
			found, specificity = press, score
		}
	}

	return found, specificity >= 0
}

// button is a beacon button that the plugin has started to watch. Owned by the plugin goroutine.
type button struct {
	config *BeaconButton
	id     string
	data   spidevice.Button

	// lastFrame is when a frame of each kind of press was last heard, which is what tells a new press from the rest of the burst
	// of one that was already announced.
	lastFrame map[spidevice.ButtonPress]time.Time

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

// startButtons registers an entity for each beacon button. Runs on the plugin goroutine.
func (p *plugin) startButtons() {
	for _, configured := range p.beaconButtons {
		b := &button{
			config:    configured,
			data:      spidevice.Button{Name: configured.name},
			lastFrame: make(map[spidevice.ButtonPress]time.Time),
			stub:      &buttonStub{},
		}

		b.stub.wrapper.Store(&spicommon.ThreadSafeEntityWrapper[spidevice.IButton]{Container: p.container, Entity: b})

		id, err := p.container.RegisterEntity(configured.address, spidevice.IButtonType, configured.name,
			func() (any, error) { return b.stub, nil })
		if err != nil {
			fmt.Printf("%T button %s (%s) could not be registered: %v\n", p, configured.name, configured.address, err)

			continue
		}

		b.id = id
		configured.entityId.Store(&id)
		p.buttons[configured.address] = b

		event := spicommon.StubAvailableEvent[spidevice.IButton]{EntityEvent: b.entityEvent(), EntityStub: b.stub}
		if err := configured.stub.Provide(p.container, event); err != nil {
			fmt.Printf("%T %v\n", p, err)
		}
	}
}

// stopButtons deregisters the buttons. Runs on the plugin goroutine.
func (p *plugin) stopButtons() {
	for address, b := range p.buttons {
		b.config.stub.Withdraw()

		if err := p.container.DeregisterEntity(b.id); err != nil {
			fmt.Printf("%T button %s could not be deregistered: %v\n", p, b.config.name, err)
		}

		b.stub.wrapper.Store(nil)
		b.config.entityId.Store(nil)
		delete(p.buttons, address)
	}
}

func (b *button) entityEvent() events.EntityEvent {
	return events.EntityEvent{Id: b.id, EntityType: spidevice.IButtonType, Name: b.config.name}
}

// hear takes in an advertisement from a beacon button's address, and announces a press if it is the first frame of one.
func (p *plugin) hearButton(b *button, a bluetooth.AdvertisementEvent) {
	frame, ok := bluetooth.ParseIBeacon(a.ManufacturerData)
	if !ok {
		return
	}

	press, ok := b.config.which(frame)
	if !ok {
		return
	}

	now := p.now()
	last, heard := b.lastFrame[press]
	b.lastFrame[press] = now

	// The rest of a burst already announced: the beacon goes on advertising a press for a few seconds.
	if heard && now.Sub(last) < b.config.burst+burstMargin {
		return
	}

	b.data.Presses++
	b.data.LastPress, b.data.LastPressTime = press, now

	fmt.Printf("%T button %s: %s press\n", p, b.config.name, press)

	p.broadcast(b.id, spidevice.ButtonPressedEvent{EntityEvent: b.entityEvent(), Press: press, Time: now, Count: b.data.Presses})
	p.broadcast(b.id, events.EntityStateChangedEvent{EntityEvent: b.entityEvent(), NewState: b.data})
}

func (p *plugin) broadcast(id string, event any) {
	if err := p.container.BroadcastEvent(id, event); err != nil {
		fmt.Printf("%T unable to broadcast %T: %v\n", p, event, err)
	}
}
