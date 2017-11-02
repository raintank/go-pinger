package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	pinger "github.com/raintank/go-pinger"
)

var (
	count     int
	timeout   time.Duration
	interval  time.Duration
	ipVersion string

	p *pinger.Pinger
)

func main() {
	flag.IntVar(&count, "count", 5, "number of pings to sent to each host (sent concurrently)")
	flag.DurationVar(&timeout, "timeout", time.Second*2, "timeout time before pings are assumed lost")
	flag.DurationVar(&interval, "interval", time.Second*10, "frequency at which the pings should be sent to hosts.")
	flag.StringVar(&ipVersion, "ipversion", "any", "ipversion to use. (v4|v6|any)")

	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("no hosts specified")
	}
	hosts := flag.Args()
	var err error
	proto := "all"
	if ipVersion == "v4" {
		proto = "ipv4"
	}
	if ipVersion == "v6" {
		proto = "ipv6"
	}
	p, err = pinger.NewPinger(proto, 1000)

	if err != nil {
		log.Fatal(err)
	}
	p.Start()

	ticker := time.NewTicker(interval)
	for range ticker.C {
		for _, host := range hosts {
			log.Printf("pinging %s", host)
			go ping(host)
		}
	}
}

func ResolveHost(host, ipversion string) (string, error) {
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) < 1 {
		return "", fmt.Errorf("failed to resolve hostname to IP.")
	}

	for _, addr := range addrs {
		if ipversion == "any" {
			return addr, nil
		}

		if strings.Contains(addr, ":") || strings.Contains(addr, "%") {
			if ipversion == "v6" {
				return addr, nil
			}
		} else {
			if ipversion == "v4" {
				return addr, nil
			}
		}
	}

	return "", fmt.Errorf("failed to resolve hostname to valid IP.")
}

func ping(host string) {
	addr, err := ResolveHost(host, ipVersion)
	if err != nil {
		log.Println(err)
		return
	}
	stats, err := p.Ping(addr, count, timeout)
	if err != nil {
		log.Println(err)
		return
	}

	total := time.Duration(0)
	min := timeout
	max := time.Duration(0)
	for _, t := range stats.Latency {
		total += t
		if t < min {
			min = t
		}
		if t > max {
			max = t
		}
	}
	avg := time.Duration(0)
	if total > 0 {
		avg = total / time.Duration(stats.Received)
	}

	log.Printf("%s sent=%d  received=%d  avg=%s min=%s max=%s\n", host, stats.Sent, stats.Received, avg.String(), min.String(), max.String())
}

type PingStats struct {
	Latency  []time.Duration
	Sent     int
	Received int
}
