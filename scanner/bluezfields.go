package scanner

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/avanha/pmaas-plugin-bluetooth/parser"
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/common"
	"github.com/avanha/pmaas-spi/bluetooth"
	dbus "github.com/godbus/dbus/v5"
)

// parseDevice has the device's parser decode what BlueZ has for it. changedPropertyName is "" to use the device's
// ManufacturerData property as it stands, or "ManufacturerData" to use newPropertyValue, which can differ from it.
//
// This is all that is particular to BlueZ about parsing: it hands over its properties as D-Bus variants, and it keeps every
// manufacturer data key it has seen during a discovery session, so for a device whose readings are keys, the parser is only
// given the ones it has not been given before.
func parseDevice(dev *common.ObservedDevice, changedPropertyName string, newPropertyValue interface{}) (bool, common.ParseResult) {
	var manufacturerData map[uint16]interface{}

	if changedPropertyName == "" {
		manufacturerData = dev.Device.Properties.ManufacturerData
	} else if changedPropertyName == "ManufacturerData" {
		manufacturerData = newPropertyValue.(map[uint16]interface{})
	}

	if manufacturerData == nil {
		return false, common.EmptyParseResult
	}

	advertised := bluetooth.Fields{ManufacturerData: make(map[uint16][]byte, len(manufacturerData))}

	for id, value := range manufacturerData {
		if data, ok := bytesOf(value); ok {
			advertised.ManufacturerData[id] = data
		}
	}

	if dev.Parser.ReadingInCompanyID {
		advertised.ManufacturerData = unseenRecords(dev, advertised.ManufacturerData)
	}

	result, ok := dev.Parser.Parse(advertised)
	if !ok {
		return false, common.EmptyParseResult
	}

	return true, toScannerResult(result)
}

// unseenRecords is the manufacturer data not handed over before, and notes it as handed over. Since a reading is in the key,
// the number of keys grows, and there is no telling what is new but by remembering. That is not foolproof: a new reading that
// repeats an old one looks old. The discovery session would have to be stopped and started now and then to flush BlueZ's cache.
func unseenRecords(dev *common.ObservedDevice, records map[uint16][]byte) map[uint16][]byte {
	if dev.SeenRecords == nil {
		dev.SeenRecords = make(map[string]bool)
	}

	var old, unseen int

	fresh := make(map[uint16][]byte)

	for id, data := range records {
		key := recordKey(id, data)

		if dev.SeenRecords[key] {
			old++

			continue
		}

		dev.SeenRecords[key] = true
		unseen++
		fresh[id] = data
	}

	fmt.Printf("%s parser saw %d old and %d new records\n", dev.Parser.Name, old, unseen)

	return fresh
}

func recordKey(id uint16, data []byte) string {
	hash := sha256.New()
	_ = binary.Write(hash, binary.LittleEndian, id)
	hash.Write(data)

	return fmt.Sprintf("%x", hash.Sum(nil))
}

func bytesOf(value interface{}) ([]byte, bool) {
	if variant, ok := value.(dbus.Variant); ok {
		if variantBytes, ok := variant.Value().([]byte); ok {
			return variantBytes, true
		}

		fmt.Printf("Unknown dbus.Variant type: %T\n", variant.Value())

		return nil, false
	}

	data, ok := value.([]byte)

	return data, ok
}

func toScannerResult(result parser.Result) common.ParseResult {
	return common.ParseResult{BatteryLevel: result.BatteryLevel, EnvironmentData: result.Environment}
}
