# Bluetooth: sources, and the plugin that makes sense of them

See also `DEVICE_MODELS.md`: the proposal to move device knowledge out of this plugin.

Status: 2026-10-17. Phases 1 to 4 are built and tested, and not yet released or run on the Pi: the contract in
`pmaas-spi/bluetooth`, the parsers, the BlueZ source, and the ESPHome proxy source, with this plugin consuming. Buttons are not. Where this
spec and that package differ, the package is right: `Fields` is embedded in `AdvertisementEvent`, the coalescer returns a
`Verdict` and the rule that matched, and `ManufacturerOf` builds a rule's optional company ID.

## Why

Today `pmaas-plugin-bluetooth` does two jobs: it talks to the local radio through BlueZ over D-Bus, and it works out what
the devices it hears are and publishes them as entities. The first is why it is Linux-only and why its parsers (Govee,
Inkbird) can't be tested without BlueZ: they read a live `Device1`.

We now want other places to hear advertisements from, starting with an ESP32 running ESPHome's Bluetooth proxy, and a
BLE button beacon used as a light switch, which has to feel instant.

So the plugin is split along the line "advertisements in, entities out":

* **Sources** hear advertisements and say so. One for the local radio (`pmaas-plugin-bluez`), one for each ESPHome
  Bluetooth proxy (in `pmaas-plugin-esphome`, which already speaks the proxies' API). Sources know nothing about what
  a device is.
* **`pmaas-plugin-bluetooth`** takes advertisements from any number of sources, works out what each device is, parses it,
  and publishes entities. It knows nothing about where an advertisement came from, beyond keeping that for display.

```
 BlueZ radio ──► pmaas-plugin-bluez ───────┐
                                           │  bluetooth.AdvertisementEvent
 ESP32 proxy ──► pmaas-plugin-esphome ─────┼────────────────────────────────►  pmaas-plugin-bluetooth ──► entities
 ESP32 proxy ──►        (same)       ──────┘                                   (parse, merge, publish)    (+ press events)
                                           ◄────────────────────────────────
                                              bluetooth.InterestEvent
```

## What was measured

From a proxy on the IoT network (ESPHome 2026.8.0, native API 1.15, encrypted), three captures over about eight minutes:

* About 22 advertisements/s, in batches of about 5, across 44 to 46 addresses. Roughly 80% of the addresses use random
  (rotating) addresses: Apple devices and neighbours' gear. Nine addresses are public: the Govee H5075s, the Inkbird,
  and a few others.
* The Govee H5075s advertise every 5 to 20 s, depending on distance; the Inkbird (`sps`) about every 2 to 3 s, with its reading in the
  manufacturer-ID bytes.
* The proxy merges an advertisement and its scan response into one record, so name and data arrive together.
* **ESPHome 2025.8 removed the parsed advertisement message.** A proxy sends only raw advertisement bytes
  (`BluetoothLERawAdvertisementsResponse`, up to 62 bytes of advertising structures per advertisement). Our ESPHome source
  therefore has to decode the structures itself. BlueZ, by contrast, hands over already-decoded properties.

The decoded-fields model is therefore the common ground, and the decoding is shared code.

## The contract: `pmaas-spi/bluetooth`

A new package, with no dependencies beyond the standard library and `pmaas-spi/events`.

### Advertisement

```go
// AdvertisementEvent is broadcast by a source for each advertisement it hears and has been asked for (see Interest).
type AdvertisementEvent struct {
    Source      string       // the source's entity ID
    Address     string       // "A4:C1:38:CC:E2:F0": upper case, colon separated
    AddressType AddressType  // AddressPublic, AddressRandom, or AddressUnknown
    RSSI        int          // dBm, as this source heard it
    Time        time.Time    // when the source received it, by its own clock

    // What this advertisement carried. A field that was not in it is absent (nil or zero), which does NOT mean the
    // device has no such thing: the consumer keeps what it has seen before (see "Merging").
    LocalName        string
    ServiceUUIDs     []string           // 128-bit, lower case: "0000ec88-0000-1000-8000-00805f9b34fb"
    ManufacturerData map[uint16][]byte  // by company ID
    ServiceData      map[string][]byte  // by 128-bit UUID
    TxPower          *int8

    // Raw is the advertising data as received, if the source has it (proxies do; BlueZ doesn't). It is for
    // diagnosis and for parsers that need bytes the fields drop.
    Raw []byte
}
```

* One event per advertisement. Events from one source are broadcast in the order they were heard.
* **Fields describe the observation, not the device.** BlueZ keeps device properties and reports changes; a proxy reports
  every advertisement. Both are translated into "what this observation carried". For BlueZ that is the changed property
  (a manufacturer-data change becomes an event with that manufacturer data and the address).
* `ParseAdvertisingData(raw []byte) (Fields, error)` in the package decodes advertising structures: flags, names,
  16/32/128-bit UUIDs, manufacturer data, service data, TX power. The ESPHome source uses it; tests use it. A malformed
  structure ends decoding with the fields so far and an error the source counts and drops, never crashes on.

### Interest

```go
// InterestEvent is broadcast by a consumer to say what it wants to hear. It replaces everything its Owner sent before.
type InterestEvent struct {
    Owner      string  // the consumer's name, so that two consumers don't replace each other
    Rules      []Rule
    ActiveScan bool    // whether any rule needs scan responses (names, for instance); sources should scan actively
}

type Rule struct {
    Name string // for logs and counters: "govee-h5075", "front-door-button"

    // Matching: every field that is set must match; an advertisement is wanted if any rule matches.
    Address        string  // exact
    NamePrefix     string
    ManufacturerID *uint16
    ManufacturerPrefix []byte // the data after the company ID starts with these bytes (empty: any)
    ServiceUUID    string

    // How eagerly to forward what matches.
    Latency Latency // Immediate or Coalesce
    Window  time.Duration // for Coalesce; zero means DefaultWindow (30 s)
}
```

* `func (r Rule) Matches(a *AdvertisementEvent) bool` and `func MatchAny(rules []Rule, a *AdvertisementEvent) *Rule` live
  in the package, so sources and consumers agree on matching, and it is tested once.
* **Replace, don't patch.** An `InterestEvent` is the whole current set. A consumer broadcasts it when it starts, whenever
  it changes, and on every minute event. A source starting later, or restarting, catches up within a minute. No source
  needs to remember to ask.
* **Sources union the rules of all owners and forget an owner not heard from for three minutes**, so a consumer that is
  removed stops costing anything.
* **Until a source has seen an interest, it forwards nothing.** (Not everything: a source that started before the
  consumer must not flood a bus that has nobody to read it.)
* Rules come from where the knowledge is: each parser declares its own rules (Govee: manufacturer ID `0xEC88`; Inkbird:
  service UUID `fff0` with name `sps`; the button: whatever its frame turns out to be), and every configured device adds an
  address rule. They can't drift from the parsers.

### Coalescing, done by every source with the same helper

`bluetooth.Coalescer` (in the package) implements one policy, so BlueZ and the proxy behave alike:

* An advertisement matching no rule is dropped.
* **Immediate:** forwarded as it comes, always. Nothing is delayed, batched or suppressed. (The volume is bounded by the
  rule: a beacon matched by address sends what it sends.)
* **Coalesce:** the first advertisement with a given address and payload is forwarded at once (the *leading edge*), and
  identical repeats within the rule's window are dropped. After the window an identical one is forwarded again, so a
  device that never changes still shows as alive. RSSI is not part of "identical".
* Counters per source: heard, matched, forwarded, malformed. They show on the source's page.

### Sources are entities

A source registers an entity of type `bluetooth.ISource` (read-only in v1):

```go
type Source struct {
    Name          string
    Kind          string       // "bluez" or "esphome-proxy"
    Connectivity  environment.Connectivity // from the transport: the proxy's connection, the adapter being up
    OnlineSince, OfflineSince time.Time
    Scanning      ScanMode     // ScanOff, ScanPassive, ScanActive
    LastHeard     time.Time
    Heard, Forwarded, Dropped uint64
}
```

* `AdvertisementEvent.Source` is that entity's ID, so a consumer can ask what a source is.
* For a proxy, the source *is* the ESPHome device; its connectivity is the device's. So the existing
  `OfflineAlert` on the device already covers "my proxy is gone", with no new code.
* The two-way direction (below) hangs its methods off this same entity.

## `pmaas-plugin-bluetooth`

* **Ingest.** Receive `AdvertisementEvent`s (a predicate on the event type, cheap). Merge into a per-address cache:
  each field with the time it was last seen, so a name from one scan response and manufacturer data from another combine,
  as BlueZ's properties did. Entries expire after an hour unseen, and the cache is bounded.
* **Identify.** A device's type is chosen once, from its merged fields, by the parsers' own matchers (today:
  `chooseParser`). Sticky, as now.
* **Parse.** A parser is a pure function, `func(*Observation) (Result, bool)`, where the observation is the merged device
  plus the advertisement that triggered it. No BlueZ, no D-Bus: the existing `scanner/parser/govee` and `inkbird` move
  here, lose `ObservedDevice`, and get tests from captured payloads.
* **Publish.** Registers entities and broadcasts changes exactly as today. `AddThermometer(address, name)` and the page
  keep working; the page gains a "heard from" column.
* **Interest.** Builds the rule set from the parsers plus the configured devices and broadcasts it as above. A button
  rule is Immediate; sensors are Coalesce.

### Merging sources

The same device is often heard by several sources.

* A device is identified by its address, never by source.
* Per device, the plugin keeps for each source its last-heard time and RSSI. The best source is the strongest one heard in
  the last minute; the page shows it, and it is useful for placing proxies.
* **One reading is processed once.** A parsed frame (address plus the bytes the parser used) seen from a second source
  within 10 s is not processed again. Otherwise two proxies would double-count and fire a button twice.
* Random addresses that rotate are out of scope for v1: the devices we care about (Govee, Inkbird, the beacon) use
  fixed addresses. They are, deliberately, what the interest rules filter out.

### Pressing a button

*Open: depends on the BC011's trigger behaviour, which we have not yet captured.* The likely shape: a parser turns the
frame (or its appearance) into a `bluetooth.ButtonPressedEvent{Address, Name, Time, Count}` once per press. A beacon
that repeats a frame for several seconds must give one event, so the parser debounces on the frame's counter if it has
one, or on the frame appearing after a gap.

Latency budget for a press, with the proxy as the source: advertisement, proxy batch (about 100 ms), native API, source,
event bus, parser, press event, and then the automation. The first four are fine. **The last one is not in scope of this
split, and is the real work:** every automation trigger is time-based and evaluated each minute. A press needs an
event-driven trigger in the automations plugin, which was not needed before and now is (decided 2026-10-17). It is its own
design and release, tracked separately; the press event is its first customer.

## `pmaas-plugin-bluez`

Everything that is BlueZ, and nothing else.

* Owns the adapter and the D-Bus discovery session (what `scanner/scanner.go` does today). Builds on Linux; on other
  systems the plugin logs once that it is unsupported and registers nothing, so one assembly can run on macOS and the Pi.
* Subscribes to `InterestEvent`. Passes what it can to BlueZ's own discovery filter (service UUIDs), and applies the rest
  and the coalescing itself, before broadcasting.
* Translates property changes into `AdvertisementEvent`s as described above. Its time is when it hears the change.
* Registers its `ISource`. Config: `Adapter` (moves here from `bluetooth`).
* Scan parameters stay BlueZ's (see "Later"). Duplicate advertisements with identical data are invisible to the D-Bus
  property model; that is a known limit for a button that repeats one frame (see Open questions).

## ESPHome proxies (in `pmaas-plugin-esphome`)

* Detected, not configured: a device whose `DeviceInfoResponse.bluetooth_proxy_feature_flags` includes raw advertisements
  is a source. `DeviceConfig`/`NativeDeviceConfig` gain `DisableBluetooth bool` to opt out.
* On each connection, once an interest exists: `SubscribeBluetoothLEAdvertisementsRequest{flags: raw}`. Re-subscribe on every
  reconnect, as entities are re-listed. When the last rule goes away, `UnsubscribeBluetoothLEAdvertisements`.
* Decode with `ParseAdvertisingData`, apply the `Coalescer`, broadcast. Runs on the connection's read loop, so it must be
  fast: no allocation per rejected advertisement beyond the decode, no blocking send. The broadcast hand-off must not
  block the loop (a slow bus makes the proxy look dead via the keepalive).
* `ActiveScan`: send `BluetoothScannerSetModeRequest` to match the interest (active when any rule needs scan responses).
  The proxy's `BluetoothScannerStateResponse` reports the mode it is really in, which the source entity shows. Interval
  and window are in the proxy's YAML and can't be set from here.
* Registers an `ISource` per proxy device, tied to the device's connectivity.
* Proxies' connections are also used for GATT (active proxy). We don't use that; it is the "later" below.

## Delivery, volume and failure

* Everything goes through `BroadcastEvent`, whose dispatch is single-threaded and ordered. At the measured 22/s before
  filtering, and a few per second after, this is not a concern. The sources' counters make a change obvious.
* A source never blocks on delivery: if the broadcast fails, count it and drop.
* A consumer that is slow shows up as the event queue growing; the existing dispatch watchdog covers it.
* A source that is down (proxy disconnected, adapter down) is visible on its entity; the consumer keeps the devices it
  knows about and ages them, as it does now.
* Clock: `Time` is the source's clock. The consumer uses its own receive time for ordering across sources and the
  source's only for display.

## Testing

* `ParseAdvertisingData` and `Rule.Matches`: table tests, and fuzzing for the decoder (malformed length bytes).
* `Coalescer`: `testing/synctest` bubbles with the virtual clock. Leading edge, window, heartbeat, Immediate.
* Parsers: pure functions over captured frames. The captures from the proxy (own devices only; neighbours' addresses
  are not kept) become fixtures, with a replay helper that feeds `AdvertisementEvent`s to the plugin.
* The `bluetooth` plugin: a fake source that broadcasts scripted events, bubble tests for merging, dedupe across sources,
  interest rebroadcast and expiry, and the source going away.
* ESPHome: the existing fake native-API device gains a Bluetooth proxy mode (batches of raw advertisements).
* An end-to-end check on the real proxy with the capture tool, kept out of the repos.

## Compatibility and release order

* `pmaas-spi` gains the `bluetooth` package: a minor bump, additive.
* `pmaas-plugin-bluetooth` loses `Adapter` and the BlueZ dependency. Assemblies add `pmaas-plugin-bluez`
  (Linux/Pi) and keep `pmaas-plugin-bluetooth`; proxies come with the `esphome` plugin by themselves. `EnableTestDevices`
  becomes a fake source in the `bluetooth` plugin's tests rather than a runtime option, or stays as a small built-in
  source; decide when we get there.
* Order: `pmaas-spi`, then `pmaas-plugin-bluetooth`, `pmaas-plugin-bluez`, `pmaas-plugin-esphome`, then the assemblies.

## Phases

Each is shippable on its own.

1. **Contract.** `pmaas-spi/bluetooth`: events, `Rule`, `Coalescer`, `ParseAdvertisingData`, with tests. No behaviour change.
   *Done, unreleased.*
2. **Parsers.** Decouple Govee and Inkbird from BlueZ, move them under the `bluetooth` plugin as pure functions with
   fixtures. The existing scanner is adapted to feed them. No behaviour change.
3. **BlueZ extracted.** `pmaas-plugin-bluez` emits advertisements and registers a source; `bluetooth` consumes events
   and publishes interest. Same devices as today, new wiring. *Done, unreleased, and not yet run against a real adapter.*
   Differences from the plan: the page does not yet show sources or a best source; a reading seen by two sources is not
   de-duplicated (harmless for sensors, which are idempotent; it matters for buttons).
4. **ESPHome proxy source.** Raw advertisements to events, scan mode, source entity. Merging across sources.
   *Done, unreleased, and not yet run against the real proxy through the plugin (the captures were).* Differences from the plan:
   each proxy has its own `Coalescer` over one shared `bluetooth.Interests`, so every proxy that hears a sensor reports it;
   a truncated final structure is trimmed away, not an error.
5. **Buttons.** The press event and its parser, once the BC011's behaviour is captured; the event-driven automation trigger
   is its own piece of work.

## Later: two-way

Nothing writes to Bluetooth today and v1 doesn't. When something must (connect, read or write a characteristic, notifications),
the PMAAS way is the one switches already use: **the capability is methods on the source's entity, and telemetry stays
events.**

```go
type ISource interface {
    GetSourceData() Source
    // later:
    Connect(ctx context.Context, address string) (Connection, error)
}
```

* A consumer finds the source from the `Source` ID on an advertisement, takes its stub, and calls it. A proxy implements
  `Connect` with the native API's Bluetooth device requests (connect, GATT read/write/notify); BlueZ with its own. Which
  source to use for a device is the consumer's choice, by RSSI and free connection slots (the proxy reports those).
* Commands never go by broadcast: they have a target and an answer.
* Nothing in v1 prevents this: the entity exists, and events carry its ID.

## Later: raw HCI

If BlueZ's scan parameters or its D-Bus property model prove too lossy for the button (identical repeated frames are
invisible to it), a third source can talk HCI directly: continuous passive scanning, controller duplicate filtering off,
and an accept list of known fixed addresses so the controller drops everything else. It needs `CAP_NET_RAW`/`CAP_NET_ADMIN`
(`setcap` on each deploy) and the adapter to itself, so it replaces `pmaas-plugin-bluez` rather than running beside it. It
would be one more source, with no change to the consumer. Not planned.

## Sightings: the list of devices around

(Added after phase 4.) Dropping what is not wanted at the source meant the page could no longer be used to find the address of a
device to configure, as it could when the plugin saw everything BlueZ did. So a consumer can also ask for *sightings*
(`InterestEvent.Sightings`, an interval): a source then broadcasts a `SightingEvent` for a device it hears that nobody wants
the advertisements of, at most that often for each device and at once when its name is first learned. A sighting says the
name, the services and the companies seen over time (so who made it), the signal and the time, and nothing of what the
device said. A `SightingTracker`, one for each place a source hears from, decides when. The plugin turns a sighting into an
advertisement with nothing to decode and ingests it, so a device is merged, identified, given a per-source signal and
aged exactly as one that is forwarded, but it is never registered or announced unless a parser recognises it.

A random address that is not static (its top two bits are not `11`) is private and changes about every fifteen minutes
(`bluetooth.IsTemporaryAddress`). Such a device is marked on the page, is dropped after ten minutes unheard, and is the
first to go when the hundred-device limit is reached. A named device from the configuration is never dropped.

## Open questions

1. **The BC011.** What its button actually sends, and whether the beacon only advertises when pressed. Needs a capture with
   presses, from a proxy with a higher scan duty cycle (the stock package scans a small fraction of the time; try
   interval 100 ms, window 60 ms and watch the proxy's Wi-Fi).
2. **Identical repeats from BlueZ.** If the button repeats one frame, BlueZ's property model hides the repeats. Then the
   press must be recognised by the frame appearing after silence, or the button must be heard by a proxy.
3. **Passive or active.** Names live in scan responses on some devices; the measured proxy merges them, so passive may
   suffice. Test before relying on it.
4. **Batching on the bus.** One event per advertisement is simplest. If the counters show trouble, batch per source, per native API message.
5. **Where the page for sources lives.** Likely a section on the `bluetooth` page.
6. **Private addresses.** Out of scope for now; if a device we care about turns out to rotate, we need its identity key.
