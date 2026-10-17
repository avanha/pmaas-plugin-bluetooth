package common

import (
	dbus "github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/bluez"
	"github.com/muka/go-bluetooth/bluez/profile/device"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-plugin-bluetooth/parser"
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

type ObservedDevice struct {
	ObjectPath       dbus.ObjectPath
	Address          string
	AddressType      string
	UUIDs            []string
	Device           *device.Device1
	PropertyChangeCh chan *bluez.PropertyChanged
	Parser           *parser.Parser

	// SeenRecords is the readings already handed to the parser, for a parser whose readings are keys of the manufacturer
	// data (see parser.Parser.ReadingInCompanyID). BlueZ keeps every key it has seen in a discovery session.
	SeenRecords  map[string]bool
	MakeAndModel string
	Type         pc.DeviceType
}
