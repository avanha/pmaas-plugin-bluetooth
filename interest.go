package bluetooth

import (
	"fmt"
	"time"

	"github.com/avanha/pmaas-plugin-bluetooth/parser"
	"github.com/avanha/pmaas-spi/bluetooth"
)

// interest is what the sources are to forward: whatever the parsers recognise, and every device the assembly named. Sensors
// repeat themselves, so they are coalesced; nothing here needs more than that yet.
func (p *plugin) interest() []bluetooth.Rule {
	rules := parser.Rules()

	for _, address := range p.configured {
		rules = append(rules, bluetooth.Rule{Name: "configured " + address, Address: address})
	}

	return rules
}

// sightingEvery is how often a source is asked to tell of a device it hears that is not otherwise wanted. Once a minute keeps
// the signal and the time it was last heard about right, for the price of a small event per device per minute.
const sightingEvery = time.Minute

// publishInterest tells the sources what is wanted. It says all of it, every time (see bluetooth.InterestEvent).
func (p *plugin) publishInterest() {
	event := bluetooth.InterestEvent{
		Owner: p.ShortName(),
		Rules: p.interest(),

		// Active scanning, for now, because a name can be in a scan response and the Inkbird is known by its name.
		ActiveScan: true,
	}

	if p.listAll {
		event.Sightings = sightingEvery
	}

	if err := bluetooth.PublishInterestEvent(p.container, event); err != nil {
		fmt.Printf("%T unable to say what is wanted: %v\n", p, err)
	}
}
