// Package parser works out what a Bluetooth device is, and decodes what it advertises. A parser is plain functions over
// what an advertisement carried (bluetooth.Fields): nothing here knows about BlueZ, D-Bus or where the advertisement
// was heard, so any source can feed it and it can be tested with captured advertisements.
package parser

import (
	pc "github.com/avanha/pmaas-plugin-bluetooth/common"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// Result is what a parser found in an advertisement.
type Result struct {
	// BatteryLevel is the battery percentage, or -1 if the advertisement did not say. A bare Result{} would read as
	// a battery that is flat, so build "found nothing" from NoReading.
	BatteryLevel int

	// Environment is the temperature and humidity, empty if the advertisement carried none.
	Environment pc.EnvironmentData
}

// NoReading is a Result that found nothing.
var NoReading = Result{BatteryLevel: -1}

// Parser knows one kind of device.
type Parser struct {
	// Name is for logs and counters.
	Name string

	// MakeAndModel is what to call the devices it identifies: "Govee GVH5075".
	MakeAndModel string

	// Type is what the devices it identifies are.
	Type pc.DeviceType

	// Rules say which advertisements might be from this kind of device, for telling the sources what to forward (see
	// bluetooth.InterestEvent). They are as narrow as identification can be done from one advertisement.
	Rules []bluetooth.Rule

	// Identifies says whether a device is one of these, from everything known about it so far.
	Identifies func(known bluetooth.Fields) bool

	// ReadingInCompanyID is that the device puts its reading where a company ID goes, so each reading is a different
	// key of ManufacturerData (the Inkbird). BlueZ keeps every key it has seen in a discovery session, so a source that
	// gets its fields from BlueZ has to hand Parse only the keys that are new.
	ReadingInCompanyID bool

	// Parse decodes one advertisement. It reports false if there was nothing it understood.
	Parse func(advertised bluetooth.Fields) (Result, bool)
}

// All is every parser, in the order they are tried.
func All() []*Parser {
	return []*Parser{Govee, Inkbird}
}

// Choose is the first parser that identifies the device, or nil.
func Choose(known bluetooth.Fields) *Parser {
	for _, p := range All() {
		if p.Identifies(known) {
			return p
		}
	}

	return nil
}

// Rules is the rules of every parser, for a consumer to tell sources what it wants.
func Rules() []bluetooth.Rule {
	var rules []bluetooth.Rule

	for _, p := range All() {
		rules = append(rules, p.Rules...)
	}

	return rules
}
