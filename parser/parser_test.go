package parser

import (
	"encoding/hex"
	"testing"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// Advertisements captured from a Bluetooth proxy, from sensors that are ours (neighbours' devices are not kept).
const (
	goveeFrame     = "0d09475648353037355f45324630030388ec02010509ff88ec000339a664001aff4c000215494e54454c4c495f524f434b535f48575075f2ff0c"
	inkbirdFrame   = "0201060302f0ff04097370730aff6607970f0099606408"
	inkbirdFrame2  = "0201060302f0ff04097370730aff8007e30e00d13c6408"
	appleFrame     = "02011a020a0c0aff4c001005031cbf6469"
	otherSpsFrame  = "0201060302f0ff04097370730aff6607970f0099606408"
	goveeNoNameAdv = "0303" + "88ec" + "09ff88ec000339a66400"
)

func fieldsOf(t *testing.T, frame string) bluetooth.Fields {
	t.Helper()

	raw, err := hex.DecodeString(frame)
	if err != nil {
		t.Fatal(err)
	}

	fields, err := bluetooth.ParseAdvertisingData(raw)
	if err != nil {
		t.Fatal(err)
	}

	return fields
}

func TestGovee_ReadsTheCapturedAdvertisement(t *testing.T) {
	result, ok := Govee.Parse(fieldsOf(t, goveeFrame))

	// 0x000339a6 is 211366: 21.1 degrees, 36.6 percent. Battery 0x64.
	if !ok || result.Environment.Temperature != 21.1 || result.Environment.Humidity != 36.6 || result.BatteryLevel != 100 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestGovee_BelowZeroTheTopBitSaysSoAndIsNotPartOfTheNumber(t *testing.T) {
	// -0.5 degrees, 12.3 percent: 5123 with the sign bit set, battery 87.
	value := uint32(0x800000 | 5123)
	data := []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value), 87}

	result, ok := Govee.Parse(bluetooth.Fields{ManufacturerData: map[uint16][]byte{0xec88: data}})
	if !ok || result.Environment.Temperature != -0.5 || result.Environment.Humidity != 12.3 || result.BatteryLevel != 87 {
		t.Errorf("%v %+v", ok, result)
	}

	// -5.1 degrees, 23.4 percent: 51234 with the sign bit set.
	value = 0x800000 | 51234
	data = []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value), 50}

	result, ok = Govee.Parse(bluetooth.Fields{ManufacturerData: map[uint16][]byte{0xec88: data}})
	if !ok || result.Environment.Temperature != -5.1 || result.Environment.Humidity != 23.4 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestGovee_NothingToReadIsNotAReadingAndShortDataDoesNotPanic(t *testing.T) {
	for name, fields := range map[string]bluetooth.Fields{
		"no manufacturer data": {},
		"another company":      {ManufacturerData: map[uint16][]byte{0x004c: {1, 2, 3, 4, 5, 6}}},
		"too short":            {ManufacturerData: map[uint16][]byte{0xec88: {0, 3, 0x39, 0xa6}}},
		"empty":                {ManufacturerData: map[uint16][]byte{0xec88: {}}},
	} {
		if result, ok := Govee.Parse(fields); ok || result != NoReading {
			t.Errorf("%s: %v %+v", name, ok, result)
		}
	}
}

func TestInkbird_ReadsTheCapturedAdvertisements(t *testing.T) {
	// The temperature is the "company ID": 0x0766 is 1894, so 18.94 degrees; 0x0f97 is 3991, so 39.91 percent.
	result, ok := Inkbird.Parse(fieldsOf(t, inkbirdFrame))
	if !ok || result.Environment.Temperature != 18.94 || result.Environment.Humidity != 39.91 || result.BatteryLevel != 100 {
		t.Errorf("%v %+v", ok, result)
	}

	result, ok = Inkbird.Parse(fieldsOf(t, inkbirdFrame2))
	if !ok || result.Environment.Temperature != 19.20 || result.Environment.Humidity != 38.11 || result.BatteryLevel != 100 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestInkbird_ATemperatureBelowZeroIsSigned(t *testing.T) {
	// 0xffe0 is -32 as a signed 16 bit number: -0.32 degrees.
	fields := bluetooth.Fields{ManufacturerData: map[uint16][]byte{0xffe0: {0x97, 0x0f, 0, 0x99, 0x60, 0x64, 0x08}}}

	result, ok := Inkbird.Parse(fields)
	if !ok || result.Environment.Temperature != -0.32 {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestInkbird_MoreThanOneReadingInOneAdvertisementCantBeToldApartSoNoneIsReported(t *testing.T) {
	reading := []byte{0x97, 0x0f, 0, 0x99, 0x60, 0x64, 0x08}
	fields := bluetooth.Fields{ManufacturerData: map[uint16][]byte{0x0766: reading, 0x0767: reading}}

	if result, ok := Inkbird.Parse(fields); ok || result != NoReading {
		t.Errorf("%v %+v", ok, result)
	}
}

func TestInkbird_OnlyDataOfTheRightLengthIsAReading(t *testing.T) {
	for name, data := range map[string][]byte{"short": {1, 2, 3}, "long": make([]byte, 8), "none": nil} {
		fields := bluetooth.Fields{ManufacturerData: map[uint16][]byte{0x0766: data}}
		if result, ok := Inkbird.Parse(fields); ok || result != NoReading {
			t.Errorf("%s: %v %+v", name, ok, result)
		}
	}

	// A reading next to a record of something else (Apple's, say) is still the one reading.
	fields := bluetooth.Fields{ManufacturerData: map[uint16][]byte{
		0x0766: {0x97, 0x0f, 0, 0x99, 0x60, 0x64, 0x08}, 0x004c: {2, 21},
	}}
	if _, ok := Inkbird.Parse(fields); !ok {
		t.Error("a reading beside another company's record was not read")
	}
}

func TestChoose_FromWhatIsKnownAboutADevice(t *testing.T) {
	for name, c := range map[string]struct {
		frame string
		want  *Parser
	}{
		"govee":              {goveeFrame, Govee},
		"inkbird":            {inkbirdFrame, Inkbird},
		"something else":     {appleFrame, nil},
		"govee's uuid alone": {goveeNoNameAdv, Govee},
	} {
		if got := Choose(fieldsOf(t, c.frame)); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}

	// The Inkbird has to say its name as well as its service: another device lists that service too.
	fields := fieldsOf(t, inkbirdFrame)
	fields.LocalName = ""

	if Choose(fields) != nil {
		t.Error("identified as an Inkbird without its name")
	}

	fields.LocalName = "tps"
	if Choose(fields) != nil {
		t.Error("identified as an Inkbird by another name")
	}

	fields = fieldsOf(t, inkbirdFrame)
	fields.ServiceUUIDs = nil

	if Choose(fields) != nil {
		t.Error("identified as an Inkbird without its service")
	}
}

func TestParsers_AreWhatTheyAreCalled(t *testing.T) {
	if Govee.Type != pc.ThermometerAndHygrometer || Govee.MakeAndModel != "Govee GVH5075" || Govee.ReadingInCompanyID {
		t.Errorf("govee %+v", Govee)
	}

	if Inkbird.Type != pc.ThermometerAndHygrometer || Inkbird.MakeAndModel != "Inkbird IBS-TH" || !Inkbird.ReadingInCompanyID {
		t.Errorf("inkbird %+v", Inkbird)
	}

	if len(All()) != 2 || All()[0] != Govee || All()[1] != Inkbird {
		t.Error("the parsers or their order")
	}
}

func TestRules_AreValidAndWantTheirOwnDevicesAndNothingElse(t *testing.T) {
	for _, rule := range Rules() {
		if err := rule.Validate(); err != nil {
			t.Errorf("%s: %v", rule.Name, err)
		}
	}

	advertised := func(frame string) *bluetooth.AdvertisementEvent {
		return &bluetooth.AdvertisementEvent{Address: "A4:C1:38:CC:E2:F0", Fields: fieldsOf(t, frame)}
	}

	wanted := func(frame string) bool {
		return bluetooth.MatchAny(Rules(), advertised(frame)) != nil
	}

	for name, c := range map[string]struct {
		frame string
		want  bool
	}{
		"govee": {goveeFrame, true}, "inkbird": {inkbirdFrame, true}, "an apple device": {appleFrame, false},
	} {
		if wanted(c.frame) != c.want {
			t.Errorf("%s: wanted %v", name, !c.want)
		}
	}
}

// The cases the Inkbird parser had tests for before it moved here, in the same numbers.
func TestInkbird_PositiveZeroAndNegativeTemperatures(t *testing.T) {
	data := []byte{237, 18, 0, 153, 47, 74, 8} // humidity 48.45, battery 74

	for name, c := range map[string]struct {
		tag  uint16
		want float32
	}{
		"positive": {uint16(int(5.36 * 100)), 5.36},
		"zero":     {0, 0},
		"negative": {0xff06, -2.5}, // -250 as an unsigned 16 bit number
	} {
		result, ok := Inkbird.Parse(bluetooth.Fields{ManufacturerData: map[uint16][]byte{c.tag: data}})
		if !ok || result.Environment.Temperature != c.want || result.Environment.Humidity != 48.45 || result.BatteryLevel != 74 {
			t.Errorf("%s: %v %+v", name, ok, result)
		}
	}
}

func TestInkbird_TheSameTemperatureWithADifferentHumidityIsADifferentReading(t *testing.T) {
	tag := uint16(int(5.36 * 100))

	first, _ := Inkbird.Parse(bluetooth.Fields{ManufacturerData: map[uint16][]byte{tag: {237, 18, 0, 153, 47, 74, 8}}})
	second, _ := Inkbird.Parse(bluetooth.Fields{ManufacturerData: map[uint16][]byte{tag: {236, 18, 0, 153, 47, 74, 8}}})

	if first.Environment.Humidity != 48.45 || second.Environment.Humidity != 48.44 {
		t.Errorf("humidity %v and %v", first.Environment.Humidity, second.Environment.Humidity)
	}
}
