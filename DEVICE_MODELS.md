# Bluetooth device models

Status: proposal, 2026-10-19. Nothing here is built.

## Goal

What a device *is* (recognising it, decoding its advertisements) moves out of this plugin into separate modules. Someone with a
new kind of device writes a module and imports it in their assembly. They don't touch this plugin or a source.

This plugin keeps everything that is the same for every device: sources in, merging, aging, interests, entities, the page.

## What leaks today

Device knowledge is in four places: `parser/` (Govee, Inkbird), `AddInkbirdThermometer` and the active-scan windows
(`NeedsScanResponse`), the KKM/Eddystone battery frames in `button.go`, and `kkm.go` in pmaas-spi.

## The model (pmaas-spi/bluetooth)

```go
// Recognises a kind of device nobody configured, and decodes it.
type DeviceModel interface {
    Name() string               // "Govee GVH5075"
    Rules() []Rule              // what sources should forward, and how fast
    NeedsScanResponse() bool    // its data is in scan responses
    Identify(known Fields) bool // from everything heard of a device so far
    New() Device
}

// A device the assembly configured, by address.
type ConfiguredDevice interface {
    Address() string
    Name() string
    Rules() []Rule
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
```

Models own their device-specific coalescing (state in `Device`, latency in `Rules`). The generic payload dedupe stays in spi's
`Coalescer`.

`Hear` runs on this plugin's goroutine, synchronously. It must be pure and fast. The plugin wraps each call in `recover` (a
panic disables that device and is logged) and warns when a call is slow.

## Config is typed, in the assembly

The assembly imports the model packages. Each has its own builders, which return a `ConfiguredDevice` and validate as they are
built, panicking on a mistake the way `regexp.MustCompile` does. The mistake is found when the assembly starts, by the person who made it.

```go
conf := bluetooth.NewPluginConfig()
conf.Recognise(govee.GVH5075, inkbird.IBSTH)               // unconfigured devices, by what they advertise
conf.Add(inkbird.Thermometer("49:22:01:23:0A:1B", "Patio")) // configured, by address
conf.Add(bluecharm.Button("DD:88:00:00:3D:36", "Couch").Double(bluetooth.MatchAnyIBeacon().WithMajor(1)))
```

A builder's result can also be the entity's handle, as `BeaconButton` is now (`EntityId()`, `GetButtonData()`).

Active-scan windows follow `NeedsScanResponse()` of the configured devices, as now.

## Registration: none

Models are passed in config, so there is no discovery between plugins, no start order, and no version check beyond the compiler.

If models ever have to arrive independently of the assembly (plugins started and stopped at runtime), they can be offered as an
entity and found the way dblog finds trackables: register for entity events, then `GetEntities`. Nothing here prevents that.

## Buttons

`Result.Presses` is in v1.

The iBeacon matching and burst logic becomes a built-in `ConfiguredDevice` in this plugin, `bluetooth.IBeaconButton`, configured the
way `AddBeaconButton` is now. iBeacon is a standard, and which major is a single or a double press is the owner's choice,
so neither is device knowledge. The plugin core only turns any model's presses into the button entity and events.

Vendor parts move: the KKM System frame (and its model/firmware) goes to a BlueCharm package that supplies battery. Other
vendors' buttons (BTHome, say) are packages that emit presses.

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
