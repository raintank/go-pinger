package pinger

import (
	"sync"
	"testing"
	"time"
)

func TestDeadline(t *testing.T) {
	pre := time.Now()
	request := &EchoRequest{
		Peer:     "127.0.0.1",
		Count:    5,
		Deadline: time.Now().Add(time.Second),
		Id:       1,
		Stats:    &PingStats{Latency: make([]time.Duration, 0), SentTime: make(map[int]time.Time)},
		Done:     make(chan *PingStats),
		Recv:     make(chan *EchoResponse),
		pinger:   NewPinger(),
	}

	go request.Listen()

	results := <-request.Done

	duration := time.Since(pre) / time.Millisecond
	if duration != 1000 {
		t.Fatalf("result should have been sent after 1000ms. Waited %d", duration)
	}
	if results.Sent != 0 {
		t.Fatalf("Sent counter should be 0")
	}
	if results.Received != 0 {
		t.Fatalf("Received counter should be 0")
	}
}

func TestBadIP(t *testing.T) {
	p := NewPinger()
	p.running = true
	_, err := p.Ping("127.0.1", 1, time.Now().Add(1*time.Second))
	if err == nil {
		t.Fatalf("Scheduling ping should have failed but it didnt.")
	}
}

func TestPing(t *testing.T) {
	p := NewPinger()
	p.Debug = true
	p.m.RLock()
	if len(p.queue) > 0 {
		t.Fatalf("queue should be empty")
	}

	if p.running {
		t.Fatalf("socket should not be active until the first ping is requested.")
	}
	p.m.RUnlock()

	c, err := p.Ping("127.0.0.1", 3, time.Now().Add(1*time.Second))
	if err != nil {
		t.Fatalf("failed to schedule ping. %s", err)
	}
	defer p.Stop()

	p.m.RLock()
	if len(p.queue) != 3 {
		t.Fatalf("queue should have 3 items. found %d", len(p.queue))
	}
	if !p.running {
		t.Fatalf("socket should be active as there are pings to listen for.")
	}
	p.m.RUnlock()

	results := <-c
	p.m.RLock()
	if len(p.queue) != 0 {
		t.Fatalf("queue should be empty")
	}
	p.m.RUnlock()

	if results.Sent != 3 {
		t.Fatalf("3 ping should have been sent")
	}

	if results.Received != 3 {
		t.Fatalf("3 ping should have been recieved")
	}

	if len(results.Latency) != 3 {
		t.Fatalf("there should be 3 latency measurements.")
	}
}

func TestConcurrentPing(t *testing.T) {
	p := NewPinger()
	p.m.RLock()
	if len(p.queue) > 0 {
		t.Fatalf("queue should be empty")
	}

	if p.running {
		t.Fatalf("socket should not be active until the first ping is requested.")
	}
	p.m.RUnlock()
	defer p.Stop()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			c, err := p.Ping("127.0.0.1", 3, time.Now().Add(1*time.Second))
			if err != nil {
				t.Fatalf("failed to schedule ping")
			}

			p.m.RLock()
			if len(p.queue) < 3 {
				t.Errorf("queue should have 3 items. found %d", len(p.queue))
			}

			if !p.running {
				t.Errorf("socket should be active as there are pings to listen for.")

			}
			p.m.RUnlock()

			results := <-c

			if results.Sent != 3 {
				t.Errorf("3 ping should have been sent, %d sent instead", results.Sent)
			}

			if results.Received != 3 {
				t.Errorf("3 ping should have been recieved, %d received instead", results.Received)
			}

			if len(results.Latency) != 3 {
				t.Errorf("there should be 3 latency measurements, %d results instead", len(results.Latency))
			}

			wg.Done()
		}()
	}
	wg.Wait()

	p.m.RLock()
	if len(p.queue) != 0 {
		t.Fatalf("queue should be empty")
	}
	p.m.RUnlock()
}
