package bluetooth

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/avanha/pmaas-spi"
	spidevice "github.com/avanha/pmaas-spi/device"
	environmental "github.com/avanha/pmaas-spi/environment"
)

var beaconTemplate = spi.TemplateInfo{
	Name:    "bluetooth_beacon",
	FuncMap: bluetoothDeviceTemplate.FuncMap,
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

	// Battery is empty until the beacon has said (see batteryLevel).
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
		Name:          b.config.name,
		Address:       b.config.address,
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

// batteryLevel is a rough battery level from a coin cell's voltage: they hold close to their rated voltage for most of their life
// and then fall away, which is why the steps are not even. It is what the beacon's owner wants to know (is it time to change
// it), not a measurement.
func batteryLevel(millivolts int) int {
	steps := [...]struct{ millivolts, percent int }{{3000, 100}, {2900, 80}, {2800, 55}, {2700, 30}, {2600, 15}, {2400, 5}, {2000, 0}}

	if millivolts >= steps[0].millivolts {
		return 100
	}

	for i := 1; i < len(steps); i++ {
		hi, lo := steps[i-1], steps[i]
		if millivolts >= lo.millivolts {
			return lo.percent + (hi.percent-lo.percent)*(millivolts-lo.millivolts)/(hi.millivolts-lo.millivolts)
		}
	}

	return 0
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
