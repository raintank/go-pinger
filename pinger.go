package pinger

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type PingStats struct {
	Latency  []time.Duration
	Sent     int
	Received int
}

/*
	A Pinger allows you to send ICMP echo requests to a large number of
	destinations concurrently.
*/
type Pinger struct {
	inFlight    map[string]*EchoRequest
	v4Conn      net.PacketConn
	v6Conn      net.PacketConn
	packetChan  chan *EchoResponse
	requestChan chan *EchoRequest
	Counter     int
	proto       string
	processWg   *sync.WaitGroup
	Debug       bool
	shutdown    bool

	sync.RWMutex
}

/*
	Creates a new Pinger instance.  Accepts the IP protocol to use "ipv4", "ipv6" or "all" and
	the number of packets to buffer in the request and response packet channels.
	The pinger instance will immediately start listening on the raw sockets (ipv4:icmp, ipv6:ipv6-icmp or both).
*/
func NewPinger(protocol string, bufferSize int) (*Pinger, error) {
	rand.Seed(time.Now().UnixNano())

	p := &Pinger{
		inFlight:    make(map[string]*EchoRequest),
		Counter:     rand.Intn(0xffff),
		proto:       protocol,
		packetChan:  make(chan *EchoResponse, bufferSize),
		requestChan: make(chan *EchoRequest, bufferSize),
		processWg:   new(sync.WaitGroup),
	}
	var err error
	switch protocol {
	case "ipv4":
		p.v4Conn, err = net.ListenPacket("ip4:icmp", "0.0.0.0")
		if err != nil {
			return nil, err
		}
	case "ipv6":
		p.v6Conn, err = net.ListenPacket("ip6:ipv6-icmp", "::")
		if err != nil {
			return nil, err
		}
	case "all":
		p.v4Conn, err = net.ListenPacket("ip4:icmp", "0.0.0.0")
		if err != nil {
			return nil, err
		}
		p.v6Conn, err = net.ListenPacket("ip6:ipv6-icmp", "::")
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid protocol, must be ipv4 or ipv6")
	}

	return p, nil

}

func (p *Pinger) Start() {
	if p.proto == "all" || p.proto == "ipv4" {
		go p.v4PacketReader()
	}
	if p.proto == "all" || p.proto == "ipv6" {
		go p.v6PacketReader()
	}
	p.processWg.Add(1)
	go p.processPkt()
}

func (p *Pinger) Stop() {
	p.Lock()
	p.shutdown = true
	p.Unlock()
	if p.v4Conn != nil {
		p.v4Conn.Close()
	}
	if p.v6Conn != nil {
		p.v6Conn.Close()
	}
	close(p.packetChan)
	p.processWg.Wait()
}

/* Send <count> icmp echo rquests to <address> and don't wait longer then <timeout> for a response.
 An error will be returned if the EchoRequests cant be sent.
 This call will block until all icmp EchoResponses are received or timeout is reached. It is safe
 to run this function in a separate goroutine.
 ```
 statsCh := make(chan *pinger.PingStats)
 errCh := make(chan error)
 go func() {
 	stats, err := p.Ping(address, count, timeout)
 	if err != nil {
		errCh <- err
 	} else {
		statsCh <- resp
 	}
 }()
 var stats *pinger.Stats
 var err error
 select {
 case stats = <- statsCh:
 case err = <- errCh:
 }
 ```
*/
func (p *Pinger) Ping(address net.IP, count int, timeout time.Duration) (*PingStats, error) {
	p.Lock()
	p.Counter++
	if p.Counter > 65535 {
		p.Counter = 0
	}
	supportedProto := p.proto
	counter := p.Counter
	p.Unlock()

	var proto icmp.Type
	proto = ipv4.ICMPTypeEcho
	if address.To4() == nil {
		proto = ipv6.ICMPTypeEchoRequest
	}

	if proto == ipv4.ICMPTypeEcho && supportedProto == "ipv6" {
		return nil, fmt.Errorf("This pinger instances does not support ipv4")
	}
	if proto == ipv6.ICMPTypeEchoRequest && supportedProto == "ipv4" {
		return nil, fmt.Errorf("This pinger instances does not support ipv6")
	}

	pingTest := make([]*EchoRequest, count)
	wg := new(sync.WaitGroup)

	p.Lock()
	for i := 0; i < count; i++ {
		wg.Add(1)
		pkt := icmp.Message{
			Type: proto,
			Code: 0,
			Body: &icmp.Echo{
				ID:   counter,
				Seq:  i,
				Data: []byte("raintank/go-pinger"),
			},
		}

		req := NewEchoRequest(pkt, address, wg)
		pingTest[i] = req

		// record our packet in the inFlight queue
		p.inFlight[req.ID] = req
	}
	p.Unlock()

	for _, req := range pingTest {
		err := p.Send(req)
		if err != nil {
			// cleanup requests from inFlightQueue
			p.Lock()
			for _, r := range pingTest {
				delete(p.inFlight, r.ID)
			}
			p.Unlock()
			return nil, err
		}
	}

	// wait for all packets to be received
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// wait for all packets to be recieved for for timeout.
	select {
	case <-done:
		if p.Debug {
			log.Printf("go-pinger: all pings are complete")
		}
	case <-time.After(timeout):
		if p.Debug {
			log.Printf("go-pinger: timeout reached")
		}
		p.Lock()
		for _, req := range pingTest {
			delete(p.inFlight, req.ID)
		}
		p.Unlock()
	}

	// calculate our timing stats.
	stats := new(PingStats)
	for _, req := range pingTest {
		stats.Sent++
		if !req.Received.IsZero() {
			stats.Received++
			stats.Latency = append(stats.Latency, req.Received.Sub(req.Sent))
		}
	}
	return stats, nil
}
