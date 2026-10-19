package bluetooth

import (
	"time"

	"github.com/avanha/pmaas-spi/bluetooth"
)

// interest is what the sources are to forward: whatever the models recognise, and every device the assembly named. Sensors
// repeat themselves, so they are coalesced unless a device's rules say otherwise (a button's frames are not).
func (p *plugin) interest() []bluetooth.Rule {
	var rules []bluetooth.Rule

	for _, model := range p.models {
		rules = append(rules, model.Rules()...)
	}

	for _, address := range p.configured {
		rules = append(rules, bluetooth.Rule{Name: "configured " + address, Address: address})
	}

	for _, device := range p.configuredDevices {
		rules = append(rules, device.Rules()...)
	}

	return rules
}

// sightingEvery is how often a source is asked to tell of a device it hears that is not otherwise wanted. Once a minute keeps
// the signal and the time it was last heard about right, for the price of a small event per device per minute.
const sightingEvery = time.Minute

// updateScanWindow opens a window of active scanning when one is due: the first at the start, and then one every scanEvery, for
// scanFor. Only when a configured device needs scan responses, and the scan is not active all the time anyway. The window is
// closed by a timer; the caller publishes the interest. Runs on the plugin goroutine.
func (p *plugin) updateScanWindow(now time.Time) {
	if !p.needsScanResponses || p.activeScan || p.windowOpen || p.scanFor <= 0 {
		return
	}

	if !p.lastWindow.IsZero() && now.Sub(p.lastWindow) < p.scanEvery-time.Second {
		return
	}

	p.windowOpen, p.lastWindow = true, now
	p.stopWindow = p.after(p.scanFor, func() {
		p.windowOpen, p.stopWindow = false, nil
		p.publishInterest()
	})

	p.log.Debug("scanning actively for a while", "for", p.scanFor)
}

// closeScanWindow ends the window and its timer. Runs on the plugin goroutine.
func (p *plugin) closeScanWindow() {
	if p.stopWindow != nil {
		p.stopWindow()
		p.stopWindow = nil
	}

	p.windowOpen = false
}

// publishInterest tells the sources what is wanted. It says all of it, every time (see bluetooth.InterestEvent).
func (p *plugin) publishInterest() {
	event := bluetooth.InterestEvent{
		Owner: p.ShortName(),
		Rules: p.interest(),

		// Passive unless the assembly asked, because a name can be in a scan response and the Inkbird is known by its name.
		ActiveScan: p.activeScan || p.windowOpen,
	}

	if p.listAll {
		event.Sightings = sightingEvery
	}

	if err := bluetooth.PublishInterestEvent(p.container, event); err != nil {
		p.log.Error("unable to say what is wanted", "error", err)
	}
}
