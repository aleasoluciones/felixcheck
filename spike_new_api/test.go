package main

import (
	"log"
	"net"
	"time"

	"github.com/aleasoluciones/goaleasoluciones/scheduledtask"
	"github.com/tatsushid/go-fastping"
)

const (
	maxPingTime = 4 * time.Second
)

func NewPingCheck(ip string) func() (bool, error) {
	return func() (bool, error) {
		var retRtt time.Duration = 0
		var isUp bool = false

		p := fastping.NewPinger()
		p.MaxRTT = maxPingTime
		ra, err := net.ResolveIPAddr("ip4:icmp", ip)

		if err != nil {
			return false, err
		}

		p.AddIPAddr(ra)
		p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
			isUp = true
			retRtt = rtt
		}

		err = p.Run()
		if err != nil {
			return false, err
		}

		return isUp, nil
	}
}

func PingCheck(ip string) (bool, error) {
	var retRtt time.Duration = 0
	var isUp bool = false

	p := fastping.NewPinger()
	p.MaxRTT = maxPingTime
	ra, err := net.ResolveIPAddr("ip4:icmp", ip)

	if err != nil {
		return false, err
	}

	p.AddIPAddr(ra)
	p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
		isUp = true
		retRtt = rtt
	}

	err = p.Run()
	if err != nil {
		return false, err
	}

	return isUp, nil
}

type CheckEngine struct {
}

func (ce CheckEngine) AddCheck(host, service string, period time.Duration, check func() (bool, error)) {
	scheduledtask.NewScheduledTask(func() {
		result, err := check()
		log.Println("Result", host, service, result, err)
	}, period, 0)
}

func main() {
	ce := CheckEngine{}

	ce.AddCheck("host1", "serv1", 5*time.Second, func() (bool, error) {
		return PingCheck("192.168.1.1")
	})

	ce.AddCheck("host2", "serv2", 5*time.Second, NewPingCheck("192.168.1.2"))
	ce.AddCheck("host3", "serv3", 5*time.Second, NewPingCheck("192.168.1.3"))

	for {
		time.Sleep(10 * time.Second)
	}
}