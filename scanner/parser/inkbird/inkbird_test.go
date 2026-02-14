package inkbird

import (
	"github.com/avanha/pmaas-plugin-bluetooth/scanner/common"
	"testing"
)

func TestParse_PositiveTemperature_Succeeds(t *testing.T) {
	dev := common.ObservedDevice{}
	manufacturerData := make(map[uint16]any)
	temp := int(5.36 * 100)
	data := []byte{237, 18, 0, 153, 47, 74, 8}
	tag := uint16(temp)
	manufacturerData[tag] = data

	success, result := Parse(&dev, "ManufacturerData", manufacturerData)

	validateParseResult(t, success, result, float32(5.36), float32(48.45), 74)
}

func TestParse_ZeroTemperature_Succeeds(t *testing.T) {
	dev := common.ObservedDevice{}
	manufacturerData := make(map[uint16]any)
	temp := int(0)
	data := []byte{237, 18, 0, 153, 47, 74, 8}
	tag := uint16(temp)
	manufacturerData[tag] = data

	success, result := Parse(&dev, "ManufacturerData", manufacturerData)

	validateParseResult(t, success, result, float32(0), float32(48.45), 74)
}

func TestParse_NegativeTemperature_Succeeds(t *testing.T) {
	dev := common.ObservedDevice{}
	manufacturerData := make(map[uint16]any)
	temp := int(-2.5 * 100)
	data := []byte{237, 18, 0, 153, 47, 74, 8}
	tag := uint16(temp)
	manufacturerData[tag] = data

	success, result := Parse(&dev, "ManufacturerData", manufacturerData)

	validateParseResult(t, success, result, float32(-2.5), float32(48.45), 74)
}

func TestParse_DuplicateRecord_NotParsed(t *testing.T) {
	dev := common.ObservedDevice{}

	temp1 := int(5.36 * 100)
	data1 := []byte{237, 18, 0, 153, 47, 74, 8}
	tag1 := uint16(temp1)
	temp2 := int(5.37 * 100)
	data2 := []byte{236, 18, 0, 153, 47, 74, 8}
	tag2 := uint16(temp2)

	manufacturerData1 := make(map[uint16]any)
	manufacturerData1[tag1] = data1
	manufacturerData1[tag2] = data2

	manufacturerData2 := make(map[uint16]any)
	manufacturerData2[tag1] = data1

	manufacturerData3 := make(map[uint16]any)
	manufacturerData3[tag2] = data2

	success, result := Parse(&dev, "ManufacturerData", manufacturerData1)

	validateParseResult(t, success, result, float32(5.36), float32(48.45), 74)

	success, result = Parse(&dev, "ManufacturerData", manufacturerData2)

	if success == true {
		t.Errorf("Second Parse of duplicate data succeded")
	}

	success, result = Parse(&dev, "ManufacturerData", manufacturerData2)

	if success == true {
		t.Errorf("Third Parse of duplicate data succeded")
	}
}

// Tests that the parser considers other differences in the data, not just the temperature.
func TestParse_RecordWithSameTemperatureDifferentHumidity_Parsed(t *testing.T) {
	dev := common.ObservedDevice{}

	temp1 := int(5.36 * 100)
	data1 := []byte{237, 18, 0, 153, 47, 74, 8}
	tag1 := uint16(temp1)
	temp2 := int(5.36 * 100)
	data2 := []byte{236, 18, 0, 153, 47, 74, 8}
	tag2 := uint16(temp2)

	manufacturerData1 := make(map[uint16]any)
	manufacturerData1[tag1] = data1

	manufacturerData2 := make(map[uint16]any)
	manufacturerData2[tag2] = data2

	success, result := Parse(&dev, "ManufacturerData", manufacturerData1)

	validateParseResult(t, success, result, float32(5.36), float32(48.45), 74)

	success, result = Parse(&dev, "ManufacturerData", manufacturerData2)

	validateParseResult(t, success, result, float32(5.36), float32(48.44), 74)
}

func validateParseResult(t *testing.T, parseSuccess bool, parseResult common.ParseResult,
	expectedTemperature float32, expectedHumidity float32, expectedBatteryLevel int) {

	if !parseSuccess {
		t.Errorf("Parse did complete successfully")
	}

	if parseResult == common.EmptyParseResult {
		t.Errorf("Parse returned EmptyParseResult")
	}

	if parseResult.EnvironmentData.Temperature != expectedTemperature {
		t.Errorf("Parse decoded invalid temperature.  Expected temp: %f, actual temp: %f",
			expectedTemperature, parseResult.EnvironmentData.Temperature)
	}

	if parseResult.EnvironmentData.Humidity != expectedHumidity {
		t.Errorf("Parse decoded invalid temperature.  Expected temp: %f, actual temp: %f",
			expectedHumidity, parseResult.EnvironmentData.Humidity)
	}

	if parseResult.BatteryLevel != expectedBatteryLevel {
		t.Errorf("Parse decoded invalid battery level.  Expected battery level: %d, actual battery level: %d",
			expectedBatteryLevel, parseResult.BatteryLevel)
	}
}
