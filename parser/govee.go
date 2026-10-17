package parser

import (
	"encoding/binary"
	"strings"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// goveeCompanyID is the company ID Govee's H5051/H5071/H5072/H5074/H5075 put their readings under, and also the 16-bit
// service UUID they list.
const goveeCompanyID = 0xEC88

// goveeUUIDPrefix is what a Govee's listed service UUID begins with.
const goveeUUIDPrefix = "0000ec88"

// Govee is the Govee GVH5075 thermometer and hygrometer. Its reading is in the manufacturer data under company ID 0xEC88:
// a 24-bit number from which the temperature and humidity are worked out, and then the battery percentage.
var Govee = &Parser{
	Name:         "govee",
	MakeAndModel: "Govee GVH5075",
	Type:         pc.ThermometerAndHygrometer,
	Rules:        []bluetooth.Rule{{Name: "govee-h5075", ServiceUUID: bluetooth.UUID16(goveeCompanyID)}},

	Identifies: func(known bluetooth.Fields) bool {
		for _, uuid := range known.ServiceUUIDs {
			if strings.HasPrefix(uuid, goveeUUIDPrefix) {
				return true
			}
		}

		return false
	},

	Parse: parseGovee,
}

func parseGovee(advertised bluetooth.Fields) (Result, bool) {
	data, ok := advertised.ManufacturerData[goveeCompanyID]

	// Four bytes of reading and one of battery. An advertisement from something else with this ID can be shorter.
	if !ok || len(data) < 5 {
		return NoReading, false
	}

	value := binary.BigEndian.Uint32(data[0:4])

	// The top bit of the 24 says the temperature is below zero, and is not part of the number.
	below := value&0x800000 != 0
	reading := value &^ 0x800000

	return Result{
		BatteryLevel: int(data[4]),
		Environment: pc.EnvironmentData{
			Temperature: goveeTemperature(reading, below),
			Humidity:    float32(reading%1000) / float32(10),
		},
	}, true
}

// goveeTemperature is the temperature in Celsius from the reading, which is the temperature in tenths of a degree
// followed by three digits of humidity.
func goveeTemperature(reading uint32, below bool) float32 {
	temp := int32(reading) / 1000

	if below {
		temp = -temp
	}

	return float32(temp) / float32(10)
}
