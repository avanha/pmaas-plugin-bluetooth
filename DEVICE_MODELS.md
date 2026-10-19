# Bluetooth device models

Status: design agreed, 2026-10-19. Nothing here is built.

## Goal

What a device *is* (recognising it, decoding its advertisements) moves out of this plugin into separate modules. Someone with a
new kind of device writes a module and imports it in their assembly. They don't touch this plugin or a source.

This plugin keeps everything that is the same for every device: sources in, merging, aging, interests, entities, the page.

## What leaks today

Device knowledge is in four places: `parser/` (Govee, Inkbird), `AddInkbirdThermometer` and the active-scan windows
(`NeedsScanResponse`), the KKM/Eddystone battery frames in `button.go`, and `kkm.go` in pmaas-spi.

## The model (pmaas-spi/bluetooth)

```go
// Recognizes a kind of device nobody configured, and decodes it.
type DeviceModel interface {
    Name() string               // "Govee GVH5075"
    Rules() []Rule              // what sources should forward, and how fast
    NeedsScanResponse() bool    // its data is in scan responses
    Identify(known Fields) bool // from everything heard of a device so far
    New() Device
}

// A device the assembly configured. Usually claimed by a fixed address; a phone's changes, so claiming is a question.
type ConfiguredDevice interface {
    Name() string
    Claims(a *AdvertisementEvent) bool // asked once per new address, then cached
    Rules() []Rule                     // may grow: a claimed address is added so it is heard often
    NeedsScanResponse() bool
    New() Device
}

// One per device. Owns that device's state: the Inkbird's changing keys, a button's burst.
type Device interface {
    Hear(a *AdvertisementEvent, now time.Time) Result
}

type Result struct {
    Battery *int          // percent
    Reading *Environment  // temperature, humidity
    Presses []ButtonPress
}
// Anything heard from a claimed device also counts as "seen", with its RSSI and source, whatever the Result says.
```

Models own their device-specific coalescing (state in `Device`, latency in `Rules`). The generic payload dedupe stays in spi's
`Coalescer`.

`Hear` runs on this plugin's goroutine, synchronously. It must be pure and fast. The plugin wraps each call in `recover` (a
panic disables that device and is logged) and warns when a call is slow.

## Recognition needs no config

A device is recognized if any model passed to `Recognize` identifies it. The devices module lists everything it supports, and
the list grows with the module, so the assembly says it once:

```go
conf := bluetooth.NewPluginConfig()
conf.Recognize(devices.All()...)   // every model in the module: a new device type needs only a newer version
```

A vendor package has its own `govee.All()` for a subset. A third-party module is one more line: `conf.Recognize(acme.All()...)`.

Config is only for what recognition can't give: a name, or a handle for automations. It is typed, in the assembly. Each package has
builders that return a `ConfiguredDevice` and validate as they are built, panicking on a mistake the way `regexp.MustCompile` does.
The mistake is found when the assembly starts, by the person who made it.

```go
conf.Add(inkbird.Thermometer("49:22:01:23:0A:1B", "Patio"))
conf.Add(bluecharm.Button("DD:88:00:00:3D:36", "Couch").Double(bluetooth.MatchAnyIBeacon().WithMajor(1)))
```

A configured device carries its own model, so it works even if that model isn't in `Recognize`. A builder's result can also be the
entity's handle, as `BeaconButton` is now (`EntityId()`, `GetButtonData()`).

Active-scan windows follow `NeedsScanResponse()` of the configured devices, as now.

## Registration: none

Models are passed in config, so there is no discovery between plugins, no start order, and no version check beyond the compiler.
Decided: an explicit `All()`, not `init()` registration. It is obvious, checked by the compiler, has no global state, and the assembly can
pick a subset when it wants to.

If models ever have to arrive independently of the assembly (plugins started and stopped at runtime), they can be offered as an
entity and found the way dblog finds trackables: register for entity events, then `GetEntities`. Nothing here prevents that.

## Buttons

`Result.Presses` is in v1.

The iBeacon matching and burst logic becomes a built-in `ConfiguredDevice` in this plugin, `bluetooth.IBeaconButton`, configured the
way `AddBeaconButton` is now. iBeacon is a standard, and which major is a single or a double press is the owner's choice,
so neither is device knowledge. The plugin core only turns any model's presses into the button entity and events.

Vendor parts move: the KKM System frame (and its model/firmware) goes to a BlueCharm package that supplies battery. Other
vendors' buttons (BTHome, say) are packages that emit presses.

## Presence (not in v1, but not boxed out)

A phone or wearable as a presence indicator splits into two parts the design already separates:

* **Identity** is a model's or builder's job: which advertisements are this device. A fixed address, an iBeacon match (a companion
  app's transmitter), or a resolvable private address checked against the owner's IRK (a standard; a helper in spi, since phones
  rotate their address about every 15 minutes and the key is the only way to follow it). Hence `Claims` and not a fixed `Address()`.
* **State** is generic and lives in this plugin: present, away or unknown, from RSSI thresholds, hysteresis and "away after N
  minutes", plus the per-source RSSI it already keeps, for "heard loudest by".

What it needs:

* A new entity kind in spi, `IPresenceSensor`: present, last seen, per-source RSSI, nearest source.
* An area for each source (`conf.Area("Garage Proxy Bluetooth", "Garage")`), so "nearest" is a room.
* A `Rule` predicate for resolvable private addresses, so sources forward the candidates, and the plugin adding a rule for an address
  that resolved, so it is heard every few seconds and not once a minute. A candidate that doesn't resolve is forgotten, not kept.
* A builder, e.g. `bluetooth.Tracker("Andre's phone").IBeacon(match).Near(-70).AwayAfter(5*time.Minute)`.

Phases: stable identities first (a companion-app iBeacon, a wearable or tag; needs no IRK), then the IRK path, then locating by
smoothing RSSI across sources. RSSI is noisy (±10 dB), so a room needs a proxy in it; home or away is much easier. Android phones
advertise little unless an app does.

## Where things live

* **pmaas-spi/bluetooth**: the interfaces above, iBeacon and Eddystone helpers, a test kit that feeds hex frames to a model.
  `kkm.go` leaves.
* **pmaas-plugin-bluetooth**: entities, page, interests, `IBeaconButton`.
* **pmaas-bluetooth-devices** (new, a library, like `pmaas-automations`): `govee/`, `inkbird/`, `bluecharm/`. A third party
  publishes its own module with the same shape.

## Phases

Each is releasable, and behaviour doesn't change until the last.

1. spi: the interfaces, the test kit, docs.
2. This plugin uses them internally; Govee, Inkbird and the iBeacon button become in-tree models and builders. The old config
   methods stay.
3. `recover`, slow-call warning.
4. New module with the three vendor packages; built-ins and the old config methods removed (breaking); assemblies import it.
5. "Writing a model" guide and a template repo.

## Open

* Entity kinds beyond sensor readings and buttons (door, motion) still need a change here and in spi. Acceptable for now.
