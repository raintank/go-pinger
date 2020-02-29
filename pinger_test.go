package pinger

import (
	"net"
	"sync"
	"testing"
	"time"
)

var localhost = net.ParseIP("127.0.0.1")

func TestBadProto(t *testing.T) {
	_, err := NewPinger("foo", 100)
	if err == nil {
		t.Fatalf("pinger should have failed to initialize")
	}
}

func TestPing(t *testing.T) {
	p, err := NewPinger("all", 100)
	if err != nil {
		t.Fatalf("failed to initialize pinger. %s", err)
	}
	p.Start()
	defer p.Stop()

	p.Debug = true
	p.RLock()
	if len(p.inFlight) > 0 {
		t.Fatalf("inFlight should be empty")
	}

	p.RUnlock()
	type resp struct {
		stats *PingStats
		err   error
	}
	respChan := make(chan resp, 10)
	go func() {
		stats, err := p.Ping(localhost, 3, time.Second)
		respChan <- resp{stats, err}
	}()

	results := <-respChan

	if results.err != nil {
		t.Fatalf("pings were not sent. %s", results.err)
	}
	p.RLock()
	if len(p.inFlight) != 0 {
		t.Fatalf("inFlight should be empty")
	}
	p.RUnlock()

	if results.stats.Sent != 3 {
		t.Fatalf("3 ping should have been sent. %d were sent instead", results.stats.Sent)
	}

	if results.stats.Received != 3 {
		t.Fatalf("3 ping should have been received. %d were received instead", results.stats.Received)
	}

	if len(results.stats.Latency) != 3 {
		t.Fatalf("there should be 3 latency measurements. Found %d instead", len(results.stats.Latency))
	}
}

func TestConcurrentPing(t *testing.T) {
	p, err := NewPinger("all", 100)
	if err != nil {
		t.Fatalf("failed to initialize pinger. %s", err)
	}
	p.Start()
	p.RLock()
	if len(p.inFlight) > 0 {
		t.Fatalf("inFlight should be empty")
	}
	p.RUnlock()
	defer p.Stop()
	var wg sync.WaitGroup
	type resp struct {
		stats *PingStats
		err   error
	}
	respChan := make(chan resp, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			stats, err := p.Ping(localhost, 3, time.Second)
			respChan <- resp{stats, err}
			wg.Done()
		}()
	}
	wg.Wait()
	close(respChan)

	p.RLock()
	if len(p.inFlight) != 0 {
		t.Fatalf("inFlight should be empty")
	}
	p.RUnlock()
	for results := range respChan {
		if results.err != nil {
			t.Fatalf("pings were not sent. %s", results.err)
		}
		if results.stats.Sent != 3 {
			t.Fatalf("3 ping should have been sent. %d were sent instead", results.stats.Sent)
		}

		if results.stats.Received != 3 {
			t.Fatalf("3 ping should have been received. %d were received instead", results.stats.Received)
		}

		if len(results.stats.Latency) != 3 {
			t.Fatalf("there should be 3 latency measurements. Found %d instead", len(results.stats.Latency))
		}
	}
}
