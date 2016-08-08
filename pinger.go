package pinger

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const ProtocolICMP = 1

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

func (e *EchoResponse) String() string {
	return fmt.Sprintf("Peer %s, Id: %d, Seq: %d, Recv: %s", e.Peer, e.Id, e.Seq, e.Received)
}

func (e *EchoRequest) Listen() {
	seqRecv := make(map[int]bool)

	timer := time.NewTimer(e.Deadline.Sub(time.Now()))
WAIT:
	for {
		select {
		case <-timer.C:
			// deadline reached.
			// log.Printf("go-pinger: deadline reached waiting for repsonse. Peer: %s, Id: %d", e.Peer, e.Id)
			break WAIT
		case resp := <-e.Recv:
			e.m.Lock()
			rtt := resp.Received.Sub(e.Stats.SentTime[resp.Seq])
			if rtt < 0 {
				rtt = 1
			}
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
				Data: []byte("raintank/go-pinger"),
			},
		}
		wb, err := pkt.Marshal(nil)
		if err != nil {
			if e.pinger.Debug {
				log.Printf("failed to marshal ICMP Echo packet. %s", err)
			}
			continue
		}
		sentTime, err := e.pinger.WritePkt(wb, e.Peer)
		if err == nil {
			e.Stats.Sent++
			e.Stats.SentTime[i] = sentTime
			if e.pinger.Debug {
				log.Printf("go-pinger: sent pkt. Peer %s, Id: %d, Seq: %d, Sent: %s", e.Peer, e.Id, i, sentTime)
			}
		}
	}
	e.Listen()
}

type Pinger struct {
	queue      map[string]chan *EchoResponse
	m          sync.RWMutex
	running    bool
	conn       net.PacketConn
	Debug      bool
	packetChan chan EchoResponse
	Counter    int
}

func NewPinger() *Pinger {
	rand.Seed(time.Now().UnixNano())
	return &Pinger{queue: make(map[string]chan *EchoResponse), Counter: rand.Intn(0xffff)}
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

	p.m.Lock()
	defer p.m.Unlock()

	p.Counter++
	if p.Counter > 65535 {
		p.Counter = 0
	}

	req := &EchoRequest{
		Peer:     address,
		Count:    count,
		Deadline: deadline,
		Id:       p.Counter,

		Done:  make(chan *PingStats),
		Recv:  make(chan *EchoResponse, count),
		Stats: &PingStats{Latency: make([]time.Duration, 0), SentTime: make(map[int]time.Time)},

		pinger: p,
	}

	for i := 0; i < count; i++ {
		key := packetKey(req.Peer, req.Id, i)
		p.queue[key] = req.Recv
	}

	if !p.running {
		log.Println("First ping, creating raw Socket.\n")
		p.start()
	}

	go req.Send()

	return req.Done, nil
}

func (p *Pinger) start() {
	c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		panic(err)
	}
	if p.Debug {
		log.Printf("Listening on socket for icmp packets.")
	}
	if c == nil {
		panic("packetConn is nil\n")
	}
	p.conn = c
	p.running = true
	p.packetChan = make(chan EchoResponse, 10000)
	go p.listenIpv4()
	go p.processPkt()
}

func (p *Pinger) Stop() {
	p.running = false
	p.conn.Close()
	if p.Debug {
		log.Printf("Socket closed.")
	}
	p.conn = nil
	close(p.packetChan)
}

func (p *Pinger) listenIpv4() {
	if p.conn == nil {
		panic("conn doesnt exist")
	}
	log.Printf("starting rawSocket listener\n")
	rb := make([]byte, 1500)
	pkt := EchoResponse{}
	var readErr error
	var data []byte
	ipconn, ok := p.conn.(*net.IPConn)
	if !ok {
		panic("connection is not IPConn")
	}
	file, err := ipconn.File()
	if err != nil {
		panic(err.Error())
	}
	defer file.Close()
	fd := file.Fd()

	var pktTime time.Time
	recvTime := syscall.Timeval{}
	for {
		if p.conn == nil {
			break
		}
		n, peer, err := p.conn.ReadFrom(rb)
		if err != nil {
			readErr = err
			break
		}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.SIOCGSTAMP), uintptr(unsafe.Pointer(&recvTime)))
		err = nil
		if errno != 0 {
			err = errno
		}
		if err == nil {
			pktTime = time.Unix(0, recvTime.Nano())
		} else {
			pktTime = time.Now()
		}

		rm, err := icmp.ParseMessage(ProtocolICMP, rb[:n])
		if err != nil {
			fmt.Println(err.Error())
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			data = rm.Body.(*icmp.Echo).Data
			if len(data) < 9 {
				log.Printf("go-pinger: invalid data payload from %s. Expected at least 9bytes got %d", peer.String(), len(data))
				continue
			}
			pkt = EchoResponse{
				Peer:     peer.String(),
				Seq:      rm.Body.(*icmp.Echo).Seq,
				Id:       rm.Body.(*icmp.Echo).ID,
				Received: pktTime,
			}
			if p.Debug {
				log.Printf("go-pinger: recieved pkt. %s\n", pkt.String())
			}
			select {
			case p.packetChan <- pkt:
			default:
				log.Printf("go-pinger: droped echo response due to blocked packetChan. %s\n", pkt.String())
			}
		}
	}
	if p.Debug {
		log.Printf("listen loop ended.")
	}
	p.m.Lock()
	if p.running {
		log.Println(readErr.Error())
		p.Stop()
		p.start()
	}
	p.m.Unlock()
}

func (p *Pinger) processPkt() {
	for pkt := range p.packetChan {
		key := packetKey(pkt.Peer, pkt.Id, pkt.Seq)
		p.m.RLock()
		req, ok := p.queue[key]
		delete(p.queue, key)
		p.m.RUnlock()
		if ok {
			if p.Debug {
				log.Printf("reply packet matches request packet. %s\n", pkt.String())
			}
			select {
			case req <- &pkt:
			default:
				log.Printf("go-pinger: droped echo response due to blocked response chan. %s", pkt.String())
			}
		} else {
			if p.Debug {
				log.Printf("go-pinger: unexpected echo response. %s\n", pkt.String())
			}
		}
	}
}

func (p *Pinger) DeleteKey(key string) {
	p.m.Lock()
	delete(p.queue, key)
	p.m.Unlock()
}

func (p *Pinger) WritePkt(b []byte, dst string) (time.Time, error) {
	if _, err := p.conn.WriteTo(b, &net.IPAddr{IP: net.ParseIP(dst)}); err != nil {
		return time.Now(), err
	}
	return time.Now(), nil
}
