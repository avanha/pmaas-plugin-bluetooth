package common

import (
	dbus "github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/bluez"
	"github.com/muka/go-bluetooth/bluez/profile/device"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
)

type ParseResult struct {
	// BatteryLevel is the parsed battery percentage, or -1 if this parse didn't produce a battery
	// reading. There's no reflection-based defaults library in this codebase, so a bare
	// ParseResult{} literal leaves this at Go's zero value, 0 - a real-looking (but wrong) reading.
	// Always build "no reading" results from EmptyParseResult rather than a zero-value literal.
	BatteryLevel    int
	EnvironmentData pc.EnvironmentData
}

var EmptyParseResult ParseResult = ParseResult{BatteryLevel: -1}

type DataParserFunc func(*ObservedDevice, string, interface{}) (bool, ParseResult)

type ObservedDevice struct {
	ObjectPath       dbus.ObjectPath
	Address          string
	AddressType      string
	UUIDs            []string
	Device           *device.Device1
	PropertyChangeCh chan *bluez.PropertyChanged
	ParseFunc        DataParserFunc
	ParserState      any
	MakeAndModel     string
	Type             pc.DeviceType
}
