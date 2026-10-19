# Bluetooth device models

Status: proposal, 2026-10-19. Nothing here is built.

## Goal

What a device *is* (recognising it, decoding its advertisements) moves out of this plugin into separate plugins. Someone with
a new kind of device writes a model and adds their plugin to the assembly. They don't touch this plugin, a source, or the
assembly's Bluetooth config beyond naming the device.

This plugin keeps everything that is the same for every device: sources in, merging, aging, interests, entities, the page.

## What leaks today

Device knowledge is in four places: `parser/` (Govee, Inkbird), `AddInkbirdThermometer` and the active-scan windows
(`NeedsScanResponse`), the KKM/Eddystone battery frames in `button.go`, and `kkm.go` in pmaas-spi.

## The model (pmaas-spi/bluetooth)

```go
type DeviceModel interface {
    ID() string                 // stable, used in config: "govee-gvh5075"
    Name() string               // "Govee GVH5075"
    Rules() []Rule              // what sources should forward, and how fast
    NeedsScanResponse() bool    // its data is in scan responses
    Identify(known Fields) bool // from everything heard of a device so far
    New(cfg DeviceConfig) (Device, error) // error: the config doesn't suit this model
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

type DeviceModelProvider interface {
    APIVersion() int
    Models() []DeviceModel   // immutable, safe from any goroutine
}
var DeviceModelProviderType = reflect.TypeFor[DeviceModelProvider]()
```

Models own their device-specific coalescing (state in `Device`, latency in `Rules`). The generic payload dedupe stays in
spi's `Coalescer`.

`Hear` runs on this plugin's goroutine, synchronously. It must be pure and fast. The plugin wraps each call in `recover` (a
panic disables that device and is logged) and warns when a call is slow.

## Registration: no ordering, no rebroadcast

Entities can't be registered in `Init`: the core starts the entity manager after every `Init`. So the models plugin registers
an entity of type `DeviceModelProviderType` in `Start`, and this plugin finds providers the way dblog finds trackables, so
the start order doesn't matter:

1. In `Start`, register a receiver for `EntityRegisteredEvent` / `EntityDeregisteredEvent` of that type.
2. Then `GetEntities` for the same type, to get the ones already there. (A provider seen by both is harmless.)

The stub is the provider itself. On a new provider: add its models, republish the interest, re-run `Identify` over devices
not yet identified. On a deregistration: drop its models; their devices keep their last state and stop being decoded.

A provider whose `APIVersion` isn't supported is logged and ignored.

## Config

`AddDevice(address, name, modelID)`. The ID is resolved against the catalog, so the assembly imports nothing from the models
plugin. An ID nobody provides is logged and shown on the page as "model X not loaded". `AddThermometer` stays for a Govee by
address; `AddInkbirdThermometer` goes.

Active-scan windows follow `NeedsScanResponse()` of the configured devices, as now.

## Buttons

`Result.Presses` is in v1.

The iBeacon matching and burst logic becomes a built-in model in this plugin, `ibeacon-button`, configured the way
`AddBeaconButton` is now. iBeacon is a standard, and which major is a single or a double press is the owner's choice,
so neither is device knowledge. The plugin core only turns any model's presses into the button entity and events.

Vendor parts move: the KKM System frame (and its model/firmware) goes to a BlueCharm model that supplies battery. Other
vendors' buttons (BTHome, say) are models that emit presses.

## Where things live

* **pmaas-spi/bluetooth**: the interfaces above, iBeacon and Eddystone helpers, a test kit that feeds hex frames to a model.
  `kkm.go` leaves.
* **pmaas-plugin-bluetooth**: catalog, entities, page, interests, `ibeacon-button`.
* **pmaas-plugin-bluetooth-devices** (new): `govee/`, `inkbird/`, `bluecharm/`. One plugin, one release. A third party writes
  `<org>-plugin-bluetooth-<vendor>`.

## Phases

Each is releasable, and behaviour doesn't change until the last.

1. spi: the interfaces, the test kit, docs.
2. This plugin uses them internally; Govee, Inkbird and `ibeacon-button` become in-tree models. Old config methods stay.
3. Provider discovery, `recover`, slow-call warning, unknown-model reporting.
4. New repo with the three vendor packages; built-ins and `AddInkbirdThermometer` removed (breaking); assemblies add the plugin.
5. "Writing a model" guide and a template repo.

## Open

* Entity kinds beyond sensor readings and buttons (door, motion) still need a change here and in spi. Acceptable for now.
* **Bad config.** Today a bad `AddBeaconButton` (no frames, two presses on one frame) panics when the config is built. With models from
  other plugins the check can only happen at `New`, after `Start`, when the model is found. So `New` returns an error: the plugin
  logs it, shows the device as "refused: reason", and carries on. Settings specific to a model (the button's frames) go in
  `DeviceConfig` as a generic map the model validates, so the assembly needn't import the model's package.
