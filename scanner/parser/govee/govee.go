package govee

import (
	"encoding/binary"
	"fmt"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/common"
	dbus "github.com/godbus/dbus/v5"
)

func Parse(dev *common.ObservedDevice, changedPropertyName string, newPropertyValue interface{}) (bool, common.ParseResult) {
	var manufacturerData map[uint16]interface{} = nil

	if changedPropertyName == "" {
		// Take the ManufacturerData value off device properties
		manufacturerData = dev.Device.Properties.ManufacturerData
	} else if changedPropertyName == "ManufacturerData" {
		// Use the manufacturer data supplied via the property change event, since it could be different than the present state
		manufacturerData = newPropertyValue.(map[uint16]interface{})
	}

	if manufacturerData == nil {
		return false, common.EmptyParseResult
	}

	var tag uint16 = 0xEC88
	packet, ok := manufacturerData[tag]

	if ok {
		bytes, bytesOk := getBytesFromData(packet)
		if bytesOk {
			result := binary.BigEndian.Uint32(bytes[0:4])
			temp := decodeTemp(result)
			humidity := float32(result%1000) / float32(10)
			batt := int(bytes[4])
			fmt.Printf("goveeParser decoded raw uint32: %v, temp: %v, humidity: %v, batt: %v\n", result, temp, humidity, batt)
			parseResult := common.ParseResult{
				BatteryLevel: batt,
				EnvironmentData: pc.EnvironmentData{
					Temperature: temp,
					Humidity:    humidity,
				},
			}
			return true, parseResult
		}
	}

	return false, common.EmptyParseResult
}

func decodeTemp(value uint32) float32 {
	var temp int32

	if (value & 0x800000) == 0 {
		temp = int32(value) / 1000
	} else {
		temp = int32(value^0x800000) / -1000
	}

	return float32(temp) / float32(10)
}

func getBytesFromData(data interface{}) ([]byte, bool) {
	if variant, ok := data.(dbus.Variant); ok {
		if variantBytes, ok := variant.Value().([]byte); ok {
			return variantBytes, true
		} else {
			fmt.Printf("Unknown dbus.Variant type: %T\n", variant.Value())
		}
	} else if dataBytes, ok := data.([]byte); ok {
		return dataBytes, true
	}

	return nil, false
}
