# A PMAAS Plugin for Bluetooth BLE Beacons

Takes Bluetooth LE advertisements from any number of *sources*, works out what the devices are (today a Govee GVH5075 and
an Inkbird IBS-TH), and publishes them as entities. It does not listen to a radio itself:

* [`pmaas-plugin-bluez`](../pmaas-plugin-bluez) hears with the local radio, through BlueZ over D-Bus (Linux).
* An ESPHome Bluetooth proxy is a source too (planned: see `DESIGN.md`).

```go
coreConfig.AddPlugin(bluez.NewPlugin(bluez.NewPluginConfig()), config.PluginConfig{}) // a source
conf := bluetooth.NewPluginConfig()
conf.AddThermometer("A4:C1:38:CC:E2:F0", "Garage") // optional: name a device you know
coreConfig.AddPlugin(bluetooth.NewPlugin(conf), config.PluginConfig{})
```

**Finding a device to configure.** The page lists every *named* device the sources hear, not only the ones that are
recognised or configured, with its address, signal and when it was last heard, which is how to find the address to give to
`AddThermometer`. The sources can't forward everything, so for a device that isn't wanted they send a *sighting* (the name, the
services and who made it, no data) at most once a minute while it is heard, and at once when its name is first learned.
Devices are kept until more than a hundred are held, when the one heard from longest ago goes. A device whose address is
about to change anyway (a private random address: phones, watches, trackers, which change theirs every quarter of an hour or
so) is marked "changes address" on the page, is let go after ten minutes without being heard, and goes first when room is
needed, so the list isn't a litter of dead phones. Set `ListAllDevices = false` to turn the list off.

It tells the sources what it wants, with an `InterestEvent` (what the parsers recognise, and every device named in the
configuration), so that the many advertisements nobody wants are dropped where they are heard. Nothing is forwarded until it
has said so, so a source added without this plugin does nothing.

The design, and what is still to come, is in `DESIGN.md`. The decoding is in `parser/`: plain functions over decoded
advertisement fields, tested with captured advertisements.

`PluginConfig.Adapter` has moved to the BlueZ plugin.

## Beacon buttons

A beacon such as the BlueCharm BC011 Pro with its triggers set is a button: `config.AddBeaconButton(address, name)` and say
which iBeacon frame is which press (`.Single(...)`, `.Double(...)`, `.Triple(...)`, `.Long(...)`). Each press is announced as a
`device.ButtonPressedEvent`, and the button is an entity (`device.IButton`) that an automation can wait for.

The devices page lists each beacon button with its address, how loudly and when it was last heard, and what was last done with
it. It also shows the battery, **if the beacon says it**: the iBeacon frames carry no battery, so give one of the beacon's slots
to an *Eddystone-TLM* frame that advertises all the time (in the KBeaconPro app, a slot of type "Eddystone TLM"; the beacon then
also stays connectable). The voltage it reports is turned into a rough percentage, since a coin cell holds its voltage until near
the end; hover over the percentage for the voltage. Until a TLM frame has been heard the page says the battery is unknown.

## Scanning

The plugin asks its sources to scan passively. Set `config.ActiveScan = true` for devices that put their names in scan responses
(the Inkbird is known by its name, and a phone is only listed once it has one); the sources then scan actively, which costs the
radio more and can miss the brief burst of a beacon button. The Govee thermometers and beacon buttons need nothing but passive.
An ESPHome proxy is switched to the mode the plugin asks for when it connects, whatever its YAML says.

## Notes

This is my oldest plugin, so it isn't exactly the be Go code or all that well organized. 

### Historical Notes

Govee is integrated in HA's ble_monitor:

https://github.com/custom-components/ble_monitor

According to the scan data, I have the H5075
https://github.com/custom-components/ble_monitor/blob/2aad98da490297f442ff9f76405446214a84fe7c/custom_components/ble_monitor/ble_parser/govee.py

masterBedroom.when().temperature().lte().farenheit(80).and().time().between(1, 7)
.then(turnOffCeilingFan)

Inkbird: 49:22:01:23:0A:1B


With tinygo-org/bluetooth I am able to get scan results, but I don't see any raw data for the advertisements.  Found this article that decodes the adveritesement, but it uses https://github.com/go-ble/ble
https://towardsdatascience.com/spelunking-bluetooth-le-with-go-c2cff65a7aca

2022-08-12 10:25 - I got the scan to work via github.com/go-ble/ble, bit I think it requires root access to run.  One more idea - try using the library that tinygo-org/bluetooth is using directly to see if the D-Bus inteface gives access to the advertising data.

Yeah, it seems like you can get/watch device properties after a device is discovered.
https://github.com/muka/go-bluetooth/blob/master/api/beacon/beacon.go


Govee example:
New device, watching for changes, name: "GVH5075_EFE0", address: A4:C1:38:4B:EF:E0, rssi: -72, addressType: public, 
UUIDs: [0000ec88-0000-1000-8000-00805f9b34fb], 
manufacturerData length: 1, manufacturerData: map[60552:@ay [0x0, 0x3, 0x7c, 0xdd, 0x64, 0x0]]

New device, watching for changes, name: "GVH5075_1CF7", address: A4:C1:38:F0:1C:F7, rssi: -72, addressType: public, 
UUIDs: [0000ec88-0000-1000-8000-00805f9b34fb], manufacturerData length: 2, manufacturerData: map[76:@ay [0x2, 0x15, 0x49, 0x4e, 0x54, 0x45, 0x4c, 0x4c, 0x49, 0x5f, 0x52, 0x4f, 0x43, 0x4b, 0x53, 0x5f, 0x48, 0x57, 0x50, 0x75, 0xf2, 0xff, 0xc] 60552:@ay [0x0, 0x3, 0x88, 0x97, 0x64, 0x0]]

```
 comp_id = (man_spec_data[3] << 8) | man_spec_data[2]
                    data_len = man_spec_data[0]

```

```
elif comp_id == 0xEC88 and data_len in [0x09, 0x0A, 0x0C, 0x22, 0x24, 0x25]:
    # Govee H5051/H5071/H5072/H5075/H5074
    sensor_data = parse_govee(self, man_spec_data, mac, rssi)
    break
```

2022-08-26 - I reworked some of the structs to use values instead of pointers after seeking time.Time.IsZero and checking https://tip.golang.org/doc/gc-guide
In the guide it says:

For instance, non-pointer Go values stored in local variables will likely not be managed by the Go GC at all 
and Go will instead arrange for memory to be allocated that's tied to the lexical scope in which it's created.
In general, this is more efficient than relying on the GC, because the Go compiler is able to predetermine when that
memory may be freed and emit machine instructions that clean up. 

A bit more reading in on the subject:

https://medium.com/a-journey-with-go/go-should-i-use-a-pointer-instead-of-a-copy-of-my-struct-44b43b104963
https://www.ardanlabs.com/blog/2017/06/design-philosophy-on-data-and-semantics.html

My conclusion is that for small structs and primitive values, it's more efficient to copy than to rely on the GC.
I did have to make some provisions 
for differentiating between supplied and default values, for example, treating the battery level int with a value of -1
as "not set" and something similar in the EnvironmentData struct.  Though there's a similar issue to the Time.IsZero
function: What if Temp and Humidity are both zero?
Perhaps there needs to be an additional bool to indicate empty/non-empty, though a temp/humidity of 0/0 should be rare. 


