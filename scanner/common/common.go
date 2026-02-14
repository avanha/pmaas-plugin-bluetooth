package common

import (
	dbus "github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/bluez"
	"github.com/muka/go-bluetooth/bluez/profile/device"

	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
)

type ParseResult struct {
	BatteryLevel    int `default:"-1"`
	EnvironmentData pc.EnvironmentData
}

var EmptyParseResult ParseResult = ParseResult{}

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
