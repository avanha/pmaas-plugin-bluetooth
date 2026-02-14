package inkbird

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"

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
		// Use the manufacturer data supplied via the property change event, since it could be different from
		// the present state
		manufacturerData = newPropertyValue.(map[uint16]interface{})
	}

	if manufacturerData == nil {
		return false, common.EmptyParseResult
	}

	var success bool = false
	var result common.ParseResult = common.EmptyParseResult
	var oldRecords, newRecords int

	// Inkbird stores the LE 16-bit integer after the length, where the tag is - so the tag is the temperature.

	for tag, packet := range manufacturerData {
		bytes, bytesOk := getBytesFromData(packet)

		if bytesOk && len(bytes) == 7 {
			// Bluez accumulates manufacturer data over the course of a discovery session.  Since the temperature
			// is stored in the tag, the number of tags grows, and it becomes impossible to know what is new, and
			// what was received previosuly.  To work around it, we'll hash the data track previously seen values
			// and ignore previously seen values.  However, that's still not foolproof since a new reading may
			// repeat the same values.  We'll need to stop and start the discovery session periodically to flush
			// the cache.
			processedData := getProcessedData(dev)
			dataHash := hashData(tag, bytes)
			alreadyProcessed := processedData[dataHash]

			if alreadyProcessed {
				oldRecords = oldRecords + 1
			} else {
				newRecords = newRecords + 1
				processedData[dataHash] = true

				if !success {
					success, result = decode(tag, bytes)
				}
			}
		}
	}

	fmt.Printf("Inkbird Parser saw %d old %s and %d new %s\n",
		oldRecords, getProperNounForRecord(oldRecords),
		newRecords, getProperNounForRecord(newRecords))

	return success, result
}

func getProperNounForRecord(count int) string {
	if count == 1 {
		return "record"
	}

	return "records"
}

func decode(tag uint16, bytes []byte) (bool, common.ParseResult) {
	//(temp, hum, probe, modbus, bat) = unpack("<hHBHB", xvalue[0:8])
	//temp := binary.LittleEndian.Uint16(fullBytes[0:2])
	// DBus already decoded the tag bytes into an unsigned value (uint16), so we need to convert it to signed value.
	temperature := int16(tag)
	humidity := binary.LittleEndian.Uint16(bytes[0:2])
	probe := bytes[2]
	modbus := binary.LittleEndian.Uint16(bytes[3:5])
	batterLevel := bytes[5]
	fmt.Printf("Inkbird Parser decoded temperature: %v, humidity: %v, probe: %v, modbus: %v batteryLevel: %v\n",
		temperature, humidity, probe, modbus, batterLevel)
	parseResult := common.ParseResult{
		BatteryLevel: int(batterLevel),
		EnvironmentData: pc.EnvironmentData{
			Temperature: float32(temperature) / float32(100),
			Humidity:    float32(humidity) / float32(100),
		},
	}
	return true, parseResult
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

func getProcessedData(dev *common.ObservedDevice) map[string]bool {
	if dev.ParserState == nil {
		processedData := make(map[string]bool)
		dev.ParserState = processedData
		return processedData
	}

	return dev.ParserState.(map[string]bool)
}

func hashData(tag uint16, bytes []byte) string {
	hash := fnv.New128()
	binary.Write(hash, binary.LittleEndian, tag)
	hash.Write(bytes)
	return fmt.Sprintf("%x", hash.Sum(nil))
}
