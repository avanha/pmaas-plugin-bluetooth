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


