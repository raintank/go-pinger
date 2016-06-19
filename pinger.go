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
)

const ProtocolICMP = 1

type pingPkt struct {
	Peer   net.Addr
	Packet *icmp.Echo
	Time   time.Time
}

type PingStats struct {
	Latency  []time.Duration
	Sent     int
	Received int
	SentTime map[int]time.Time
}

type EchoRequest struct {
	Peer     string
	Count    int
	Deadline time.Time
	Id       int
	Stats    *PingStats
	Done     chan *PingStats
	Recv     chan *EchoResponse

	m      sync.RWMutex
	pinger *Pinger
}

type EchoResponse struct {
	Peer     string
	Id       int
	Seq      int
	Received time.Time
}

func (e *EchoRequest) Listen() {
	seqRecv := make(map[int]bool)

	timer := time.NewTimer(e.Deadline.Sub(time.Now()))
WAIT:
	for {
		select {
		case <-timer.C:
			// deadline reached.
			break WAIT
		case resp := <-e.Recv:
			e.m.Lock()
			rtt := resp.Received.Sub(e.Stats.SentTime[resp.Seq])
			e.Stats.Received++
			e.Stats.Latency = append(e.Stats.Latency, rtt)
			e.m.Unlock()

			seqRecv[resp.Seq] = true
			// we have recieved responses for all pings sent.
			if e.Stats.Received >= e.Stats.Sent {
				break WAIT
			}
		}
	}
	timer.Stop()
	for i := 0; i < e.Count; i++ {
		if _, ok := seqRecv[i]; !ok {
			key := packetKey(e.Peer, e.Id, i)
			e.pinger.DeleteKey(key)
		}
	}

	e.Done <- e.Stats
}

func (e *EchoRequest) Send() {
	for i := 0; i < e.Count; i++ {
		pkt := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   e.Id,
				Seq:  i,
				Data: []byte("raintank-Litmus"),
			},
		}
		wb, err := pkt.Marshal(nil)
		if err != nil {
			if e.pinger.Debug {
				log.Printf("failed to marshal ICMP Echo packet. %s", err)
			}
			continue
		}
		e.m.Lock()
		e.Stats.SentTime[i] = time.Now()
		e.Stats.Sent++
		e.m.Unlock()
		e.pinger.WritePkt(wb, e.Peer)
	}
}

type Pinger struct {
	queue      map[string]chan *EchoResponse
	m          sync.RWMutex
	running    bool
	conn       *icmp.PacketConn
	Debug      bool
	packetChan chan *pingPkt
}

func NewPinger() *Pinger {
	rand.Seed(time.Now().UnixNano())
	return &Pinger{queue: make(map[string]chan *EchoResponse)}
}

func packetKey(addr string, id, seq int) string {
	return fmt.Sprintf("%s-%d-%d", addr, id, seq)
}

func (p *Pinger) Ping(address string, count int, deadline time.Time) (<-chan *PingStats, error) {
	// ensuire the IP address is valid.
	ipAddr := net.ParseIP(address)
	if ipAddr == nil {
		return nil, fmt.Errorf("Failed to parse IP address")
	}

	req := &EchoRequest{
		Peer:     address,
		Count:    count,
		Deadline: deadline,
		Id:       rand.Intn(0xffff),

		Done:  make(chan *PingStats),
		Recv:  make(chan *EchoResponse, count),
		Stats: &PingStats{Latency: make([]time.Duration, 0), SentTime: make(map[int]time.Time)},

		pinger: p,
	}

	p.m.Lock()
	defer p.m.Unlock()

	for i := 0; i < count; i++ {
		key := packetKey(req.Peer, req.Id, i)
		p.queue[key] = req.Recv
	}

	if !p.running {
		p.start()
	}

	go req.Listen()
	go req.Send()

	return req.Done, nil
}

func (p *Pinger) start() {
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		panic(err)
	}
	if p.Debug {
		log.Printf("Listening on socket for ip4:icmp packets.")
	}
	p.conn = c
	p.running = true
	p.packetChan = make(chan *pingPkt, 1000)
	go p.listenIpv4()
	go p.processPkt()
}

func (p *Pinger) stop() {
	p.running = false
	p.conn.Close()
	if p.Debug {
		log.Printf("Socket closed.")
	}
	p.conn = nil
	close(p.packetChan)
}

func (p *Pinger) listenIpv4() {
	for {
		rb := make([]byte, 1500)
		n, peer, err := p.conn.ReadFrom(rb)
		pktTime := time.Now()
		if err != nil {
			log.Println(err.Error())
			break
		}
		rm, err := icmp.ParseMessage(ProtocolICMP, rb[:n])
		if err != nil {
			fmt.Println(err.Error())
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			if p.Debug {
				log.Printf("recieved Echo Reply from %s\n", peer.String())
			}
			p.packetChan <- &pingPkt{
				Peer:   peer,
				Packet: rm.Body.(*icmp.Echo),
				Time:   pktTime,
			}
		}
	}
	if p.Debug {
		log.Printf("listen loop ended.")
	}
	p.m.Lock()
	if p.running {
		p.stop()
		p.start()
	}
	p.m.Unlock()
}

func (p *Pinger) processPkt() {
	for pkt := range p.packetChan {
		key := packetKey(pkt.Peer.String(), pkt.Packet.ID, pkt.Packet.Seq)
		p.m.RLock()
		req, ok := p.queue[key]
		p.m.RUnlock()
		if ok {
			//delete this packets key from the queue
			p.m.Lock()
			delete(p.queue, key)
			if len(p.queue) < 1 {
				if p.Debug {
					log.Printf("queue is empty, closing socket.")
				}
				p.stop()
			}
			p.m.Unlock()

			if p.Debug {
				log.Printf("reply packet matches request packet. %s - %d:%d\n", pkt.Peer.String(), pkt.Packet.ID, pkt.Packet.Seq)
			}

			resp := &EchoResponse{
				Peer:     pkt.Peer.String(),
				Id:       pkt.Packet.ID,
				Seq:      pkt.Packet.Seq,
				Received: pkt.Time,
			}
			req <- resp
		}
	}
}

func (p *Pinger) DeleteKey(key string) {
	p.m.Lock()
	defer p.m.Unlock()
	delete(p.queue, key)
}

func (p *Pinger) WritePkt(b []byte, dst string) {
	if _, err := p.conn.WriteTo(b, &net.IPAddr{IP: net.ParseIP(dst)}); err != nil {
		if p.Debug {
			fmt.Printf("Failed to write packet to socket. %s", err)
		}
	}
}
