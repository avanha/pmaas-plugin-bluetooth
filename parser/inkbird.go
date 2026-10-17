package parser

import (
	"encoding/binary"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// inkbirdServiceUUID is the service the IBS-TH lists.
const inkbirdServiceUUID = "0000fff0-0000-1000-8000-00805f9b34fb"

// inkbirdName is the name the IBS-TH advertises.
const inkbirdName = "sps"

// inkbirdReadingLength is how long the data under the "company ID" is.
const inkbirdReadingLength = 7

// Inkbird is the Inkbird IBS-TH thermometer and hygrometer. It puts its reading where a company ID goes: the 16-bit
// signed temperature in hundredths of a degree is the "company ID", and the humidity, probe, modbus and battery follow.
// So every reading is a different key.
var Inkbird = &Parser{
	Name:         "inkbird",
	MakeAndModel: "Inkbird IBS-TH",
	Type:         pc.ThermometerAndHygrometer,
	Rules:        []bluetooth.Rule{{Name: "inkbird-ibs-th", ServiceUUID: inkbirdServiceUUID, NamePrefix: inkbirdName}},

	Identifies: func(known bluetooth.Fields) bool {
		if known.LocalName != inkbirdName {
			return false
		}

		for _, uuid := range known.ServiceUUIDs {
			if uuid == inkbirdServiceUUID {
				return true
			}
		}

		return false
	},

	ReadingInCompanyID: true,

	Parse: parseInkbird,
}

// parseInkbird decodes the reading, if there is exactly one in the advertisement. With more than one it can't tell which is
// the latest, and a guess risks reporting a stale reading, so it waits for the next.
func parseInkbird(advertised bluetooth.Fields) (Result, bool) {
	var tag uint16
	var data []byte
	var found int

	for key, bytes := range advertised.ManufacturerData {
		if len(bytes) == inkbirdReadingLength {
			tag, data = key, bytes
			found++
		}
	}

	if found != 1 {
		return NoReading, false
	}

	// (temp, hum, probe, modbus, bat) = unpack("<hHBHB", ...). The temperature is the "company ID", unsigned as we read it.
	temperature := int16(tag)
	humidity := binary.LittleEndian.Uint16(data[0:2])

	return Result{
		BatteryLevel: int(data[5]),
		Environment: pc.EnvironmentData{
			Temperature: float32(temperature) / float32(100),
			Humidity:    float32(humidity) / float32(100),
		},
	}, true
}
