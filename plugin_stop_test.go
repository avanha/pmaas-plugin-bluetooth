package bluetooth

import (
	"testing"
	"time"

	spi "github.com/avanha/pmaas-spi"
)

type stopContainer struct {
	spi.IPMAASContainer
}

func (stopContainer) ClosedCallbackChannel() chan func() {
	ch := make(chan func())
	close(ch)

	return ch
}

func newStopTestPlugin() *plugin {
	return &plugin{state: &state{container: stopContainer{}}}
}

func isClosed(ch chan func()) bool {
	select {
	case _, open := <-ch:
		return !open
	default:
		return false
	}
}

// Stop runs on the plugin goroutine, so it must return promptly however long the run takes to wind
// down, and signal completion by closing the channel it returns.
func TestStop_DoesNotBlockWhileTheRunWindsDown(t *testing.T) {
	p := newStopTestPlugin()

	release := make(chan struct{})
	finished := make(chan struct{})

	p.state.scanCancelFunc = func() {
		<-release
		close(finished)
	}

	returned := make(chan chan func(), 1)
	go func() { returned <- p.Stop() }()

	var done chan func()

	select {
	case done = <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on the run winding down")
	}

	if isClosed(done) {
		t.Fatal("the channel was closed before the run finished")
	}

	close(release)

	select {
	case _, open := <-done:
		if open {
			t.Fatal("unexpected callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel was not closed after the run finished")
	}

	select {
	case <-finished:
	default:
		t.Fatal("the channel was closed before the run finished")
	}
}

func TestStop_WithNothingRunningReturnsAClosedChannel(t *testing.T) {
	p := newStopTestPlugin()

	if done := p.Stop(); !isClosed(done) {
		t.Fatal("expected a closed channel when the run was never started")
	}
}

func TestStop_CalledTwiceStopsTheRunOnce(t *testing.T) {
	p := newStopTestPlugin()

	calls := 0
	p.state.scanCancelFunc = func() { calls++ }

	first := p.Stop()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("the first Stop never completed")
	}

	if second := p.Stop(); !isClosed(second) {
		t.Fatal("expected the second Stop to return a closed channel")
	}

	if calls != 1 {
		t.Fatalf("expected the run to be stopped once, got %d", calls)
	}
}
