package bluetooth

import (
	"errors"
	"fmt"
	"github.com/avanha/pmaas-spi/format"
	"io"
	"reflect"
	"text/template"
	"time"

	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/bluetooth"
	spidevice "github.com/avanha/pmaas-spi/device"
	environmental "github.com/avanha/pmaas-spi/environment"
)

var beaconTemplate = spi.TemplateInfo{
	Name:    "bluetooth_beacon",
	FuncMap: template.FuncMap{"RelativeTime": format.Terse, "BatteryIcon": batteryIcon, "IsLowBattery": isLowBattery},
	Paths:   []string{"templates/bluetooth_beacon.htmlt"},
	Styles:  []string{"css/bluetooth_device.css"},
}

var publishedBeaconType = reflect.TypeOf((*publishedBeacon)(nil)).Elem()

// publishedBeacon is a beacon button as the page shows it: what it is, how well it is heard, how it is getting on, and what
// was last done with it.
type publishedBeacon struct {
	Name    string
	Address string

	// Kind says what sort of thing it is, since an address and a name don't.
	Kind string

	// Heard is whether anything has been heard from the beacon since the plugin started, and LastHeard when, and RSSI how loudly.
	// A beacon that is set only to advertise when pressed is silent until it is.
	Heard     bool
	LastHeard time.Time
	RSSI      environmental.RSSIData

	// Battery is empty until the beacon has said (see Result.Battery).
	Battery    environmental.BatteryData
	Millivolts int

	Presses       uint64
	LastPress     string
	LastPressTime time.Time
}

func (b publishedBeacon) SortKey() string {
	return b.Name
}

// publishBeacon is the page's view of a beacon button. Runs on the plugin goroutine.
func (b *button) publish() publishedBeacon {
	view := publishedBeacon{
		Name:          b.name,
		Address:       b.config.(bluetooth.Addressed).Address(),
		Kind:          "Beacon button",
		Heard:         !b.lastHeard.IsZero(),
		LastHeard:     b.lastHeard,
		Battery:       b.battery,
		Millivolts:    b.millivolts,
		Presses:       b.data.Presses,
		LastPressTime: b.data.LastPressTime,
	}

	if view.Heard {
		view.RSSI = environmental.RSSIData{RSSI: b.rssi, LastUpdateTime: b.lastHeard}
	}

	if b.data.LastPress != spidevice.PressUnknown {
		view.LastPress = b.data.LastPress.String()
	}

	return view
}

// batteryIcon is the Bootstrap icon that looks like a battery with this much in it, and isLowBattery whether it is time to change it.
func batteryIcon(level int) string {
	switch {
	case level >= 70:
		return "battery-full"
	case level >= 30:
		return "battery-half"
	default:
		return "battery"
	}
}

func isLowBattery(level int) bool {
	return level < 20
}

var thermometerTemplate = spi.TemplateInfo{
	Name: "bluetooth_thermometer",
	FuncMap: template.FuncMap{
		"RelativeTime":        format.Terse,
		"BatteryIcon":         batteryIcon,
		"IsLowBattery":        isLowBattery,
		"CelsiusToFahrenheit": func(c float32) float32 { return c*9/5 + 32 },
	},
	Paths:  []string{"templates/bluetooth_thermometer.htmlt"},
	Styles: []string{"css/bluetooth_device.css"},
}

var publishedThermometerType = reflect.TypeOf((*publishedWirelessThermometer)(nil)).Elem()

func (p *plugin) thermometerRendererFactory() (spi.EntityRenderer, error) {
	t, err := p.container.GetTemplate(&thermometerTemplate)
	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("unable to load bluetooth_thermometer template: %v", err)
	}

	renderer := func(w io.Writer, entity any) error {
		thermometer, ok := entity.(publishedWirelessThermometer)
		if !ok {
			return errors.New("item is not an instance of publishedWirelessThermometer")
		}

		if err := t.Instance.Execute(w, thermometer); err != nil {
			return fmt.Errorf("unable to execute bluetooth_thermometer template: %w", err)
		}

		return nil
	}

	return spi.EntityRenderer{StreamingRenderFunc: renderer, Styles: t.Styles, Scripts: t.Scripts}, nil
}

func (p *plugin) beaconRendererFactory() (spi.EntityRenderer, error) {
	t, err := p.container.GetTemplate(&beaconTemplate)
	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("unable to load bluetooth_beacon template: %v", err)
	}

	renderer := func(w io.Writer, entity any) error {
		beacon, ok := entity.(publishedBeacon)
		if !ok {
			return errors.New("item is not an instance of publishedBeacon")
		}

		if err := t.Instance.Execute(w, beacon); err != nil {
			return fmt.Errorf("unable to execute bluetooth_beacon template: %w", err)
		}

		return nil
	}

	return spi.EntityRenderer{StreamingRenderFunc: renderer, Styles: t.Styles, Scripts: t.Scripts}, nil
}
