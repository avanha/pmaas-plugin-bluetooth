package scanner

import (
	"testing"

	"github.com/avanha/pmaas-plugin-bluetooth/parser"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/common"
	dbus "github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/bluez/profile/device"
)

func deviceWith(p *parser.Parser, manufacturerData map[uint16]interface{}) *common.ObservedDevice {
	return &common.ObservedDevice{
		Parser: p,
		Device: &device.Device1{Properties: &device.Device1Properties{ManufacturerData: manufacturerData}},
	}
}

func reading(temperature byte) []byte {
	// Humidity 39.91 (0x0f97), probe 0, modbus 0x6099, battery 100, and a trailing byte.
	return []byte{0x97, 0x0f, 0x00, 0x99, 0x60, 0x64, temperature}
}

func TestParseDevice_AGoveeFromTheDevicesOwnProperties(t *testing.T) {
	dev := deviceWith(parser.Govee, map[uint16]interface{}{
		0xec88: dbus.MakeVariant([]byte{0x00, 0x03, 0x39, 0xa6, 0x64, 0x00}),
		0x004c: dbus.MakeVariant([]byte{0x02, 0x15}),
	})

	ok, result := parseDevice(dev, "", nil)
	if !ok || result.EnvironmentData.Temperature != 21.1 || result.EnvironmentData.Humidity != 36.6 || result.BatteryLevel != 100 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestParseDevice_AChangedPropertyIsWhatIsParsedNotWhatTheDeviceHeldBefore(t *testing.T) {
	dev := deviceWith(parser.Govee, map[uint16]interface{}{0xec88: dbus.MakeVariant([]byte{0, 0, 0, 0, 1})})

	ok, result := parseDevice(dev, "ManufacturerData",
		map[uint16]interface{}{0xec88: dbus.MakeVariant([]byte{0x00, 0x03, 0x39, 0xa6, 0x64, 0x00})})
	if !ok || result.EnvironmentData.Temperature != 21.1 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestParseDevice_OtherPropertiesAndNoDataAreNothingToParse(t *testing.T) {
	dev := deviceWith(parser.Govee, nil)

	if ok, _ := parseDevice(dev, "", nil); ok {
		t.Error("parsed a device with no manufacturer data")
	}

	if ok, _ := parseDevice(dev, "RSSI", int16(-50)); ok {
		t.Error("parsed a property that is not manufacturer data")
	}
}

func TestParseDevice_ValuesThatAreNotBytesAreLeftOut(t *testing.T) {
	dev := deviceWith(parser.Govee, map[uint16]interface{}{
		0xec88: dbus.MakeVariant("not bytes"),
		0x004c: 7,
	})

	if ok, _ := parseDevice(dev, "", nil); ok {
		t.Error("parsed something that was not bytes")
	}
}

func TestParseDevice_BlueZKeepsEveryKeyItHasSeenSoOnlyNewReadingsAreParsed(t *testing.T) {
	dev := deviceWith(parser.Inkbird, nil)

	records := func(tags ...uint16) map[uint16]interface{} {
		m := make(map[uint16]interface{})
		for _, tag := range tags {
			m[tag] = dbus.MakeVariant(reading(0x08))
		}

		return m
	}

	// The first time, three old readings are there already and there is no telling which is the latest: skipped.
	if ok, _ := parseDevice(dev, "ManufacturerData", records(0x0760, 0x0761, 0x0762)); ok {
		t.Error("guessed among three")
	}

	// One new key among those it knows: that is the reading.
	ok, result := parseDevice(dev, "ManufacturerData", records(0x0760, 0x0761, 0x0762, 0x0766))
	if !ok || result.EnvironmentData.Temperature != 18.94 || result.EnvironmentData.Humidity != 39.91 {
		t.Errorf("%v %+v", ok, result)
	}

	// Nothing new: nothing to report.
	if ok, _ := parseDevice(dev, "ManufacturerData", records(0x0760, 0x0761, 0x0762, 0x0766)); ok {
		t.Error("reported a reading already reported")
	}

	// And the next new one.
	ok, result = parseDevice(dev, "ManufacturerData", records(0x0760, 0x0761, 0x0762, 0x0766, 0x0767))
	if !ok || result.EnvironmentData.Temperature != 18.95 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestParseDevice_AGoveeIsNotHeldToThatBecauseItsReadingIsNotInTheKey(t *testing.T) {
	dev := deviceWith(parser.Govee, nil)
	m := map[uint16]interface{}{0xec88: dbus.MakeVariant([]byte{0x00, 0x03, 0x39, 0xa6, 0x64, 0x00})}

	for range 3 {
		if ok, _ := parseDevice(dev, "ManufacturerData", m); !ok {
			t.Fatal("a repeated Govee reading was dropped")
		}
	}
}

// From the Inkbird parser's own tests before it moved: records that arrive together, and a skipped parse is not a battery of 0.
func TestParseDevice_TwoNewRecordsAtOnceAreNotGuessedAmongAndAreNotParsedLater(t *testing.T) {
	dev := deviceWith(parser.Inkbird, nil)

	first := []byte{237, 18, 0, 153, 47, 74, 8}
	second := []byte{236, 18, 0, 153, 47, 74, 8}

	both := map[uint16]interface{}{536: dbus.MakeVariant(first), 537: dbus.MakeVariant(second)}
	one := map[uint16]interface{}{536: dbus.MakeVariant(first)}

	for name, m := range map[string]map[uint16]interface{}{"both at once": both, "one of them again": one, "and again": one} {
		ok, result := parseDevice(dev, "ManufacturerData", m)
		if ok || result.BatteryLevel != -1 {
			t.Errorf("%s: %v %+v", name, ok, result)
		}
	}
}

func TestParseDevice_TheSameTemperatureWithADifferentHumidityIsNew(t *testing.T) {
	dev := deviceWith(parser.Inkbird, nil)

	first := map[uint16]interface{}{536: dbus.MakeVariant([]byte{237, 18, 0, 153, 47, 74, 8})}
	second := map[uint16]interface{}{536: dbus.MakeVariant([]byte{236, 18, 0, 153, 47, 74, 8})}

	ok, result := parseDevice(dev, "ManufacturerData", first)
	if !ok || result.EnvironmentData.Humidity != 48.45 {
		t.Fatalf("%v %+v", ok, result)
	}

	ok, result = parseDevice(dev, "ManufacturerData", second)
	if !ok || result.EnvironmentData.Humidity != 48.44 {
		t.Errorf("%v %+v", ok, result)
	}
}
