package bluetooth

import (
	"fmt"

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

// publishInterest tells the sources what is wanted. It says all of it, every time (see bluetooth.InterestEvent).
func (p *plugin) publishInterest() {
	// Active scanning, for now, because a name can be in a scan response and the Inkbird is known by its name.
	if err := bluetooth.PublishInterest(p.container, p.ShortName(), p.interest(), true); err != nil {
		fmt.Printf("%T unable to say what is wanted: %v\n", p, err)
	}
}
