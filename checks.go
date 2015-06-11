package felixcheck

import (
	"fmt"
	"net"
	"strings"
	"time"

	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/streadway/amqp"
	"github.com/tatsushid/go-fastping"
)

import (
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
)

const (
	sysName     = "1.3.6.1.2.1.1.5.0"
	maxPingTime = 4 * time.Second
)

type CheckFunction func() Event
type MultiCheckFunction func() []Event

func (f CheckFunction) Tags(tags ...string) CheckFunction {
	return func() Event {
		result := f()
		result.Tags = tags
		return result
	}
}

func (f CheckFunction) Attributes(attributes map[string]string) CheckFunction {
	return func() Event {
		result := f()
		result.Attributes = attributes
		return result
	}
}

func (f CheckFunction) Ttl(ttl float32) CheckFunction {
	return func() Event {
		result := f()
		result.Ttl = ttl
		return result
	}
}

func (f CheckFunction) Retry(times int, sleep time.Duration) CheckFunction {
	return func() Event {
		var result Event
		for i := 0; i < times; i++ {
			result = f()
			if result.State == "ok" {
				return result
			}
			time.Sleep(sleep)
		}
		return result
	}
}

func NewPingChecker(host, service, ip string) CheckFunction {
	return func() Event {
		var retRtt time.Duration = 0
		var result Event = Event{Host: host, Service: service, State: "critical"}

		p := fastping.NewPinger()
		p.MaxRTT = maxPingTime
		ra, err := net.ResolveIPAddr("ip4:icmp", ip)
		if err != nil {
			result.Description = err.Error()
		}

		p.AddIPAddr(ra)
		p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
			result.State = "ok"
			result.Metric = float32(retRtt.Nanoseconds() / 1e6)
		}

		err = p.Run()
		if err != nil {
			result.Description = err.Error()
		}
		return result
	}
}

func NewTcpPortChecker(host, service, ip string, port int, timeout time.Duration) CheckFunction {
	return func() Event {
		var err error
		var conn net.Conn

		var t1 = time.Now()
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), timeout)
		if err == nil {
			conn.Close()
			milliseconds := float32((time.Now().Sub(t1)).Nanoseconds() / 1e6)
			return Event{Host: host, Service: service, State: "ok", Metric: milliseconds}
		}
		return Event{Host: host, Service: service, State: "critical"}
	}
}

type ValidateHttpResponseFunction func(resp *http.Response) (state, description string)

func BodyGreaterThan(minLength int) ValidateHttpResponseFunction {
	return func(httpResp *http.Response) (state, description string) {
		if httpResp.StatusCode != 200 {
			return "critical", fmt.Sprintf("Response %d", httpResp.StatusCode)
		}
		if httpResp.Body == nil {
			return "critical", fmt.Sprintf("Empty body")
		}
		body, err := ioutil.ReadAll(httpResp.Body)
		if err != nil {
			return "critical", fmt.Sprintf("Error geting body")
		}
		if len(body) < minLength {
			return "critical", fmt.Sprintf("Obtained %d bytes, expected more than %d", len(body), minLength)
		} else {
			return "ok", ""
		}
	}
}

func NewGenericHttpChecker(host, service, url string, validationFunc ValidateHttpResponseFunction) CheckFunction {
	return func() Event {
		var t1 = time.Now()

		response, err := http.Get(url)
		milliseconds := float32((time.Now().Sub(t1)).Nanoseconds() / 1e6)
		result := Event{Host: host, Service: service, State: "critical", Metric: milliseconds}
		if err != nil {
			result.Description = err.Error()
		} else {
			if response.Body != nil {
				defer response.Body.Close()
			}
			result.State, result.Description = validationFunc(response)
		}
		return result
	}
}

func NewHttpChecker(host, service, url string, expectedStatusCode int) CheckFunction {
	return NewGenericHttpChecker(host, service, url,
		func(httpResp *http.Response) (string, string) {
			if httpResp.StatusCode == expectedStatusCode {
				return "ok", ""
			} else {
				return "critical", fmt.Sprintf("Response %d", httpResp.StatusCode)
			}
		})
}

type SnmpCheckerConf struct {
	retries    int
	timeout    time.Duration
	oidToCheck string
}

var DefaultSnmpCheckConf = SnmpCheckerConf{
	retries:    1,
	timeout:    1 * time.Second,
	oidToCheck: sysName,
}

func NewSnmpChecker(host, service, ip, community string, conf SnmpCheckerConf) CheckFunction {
	return func() Event {

		_, err := snmpGet(ip, community, []string{conf.oidToCheck}, conf.timeout, conf.retries)
		if err == nil {
			return Event{Host: host, Service: service, State: "ok", Description: err.Error()}
		} else {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
	}
}

func NewC4CMTSTempChecker(host, service, ip, community string, maxAllowedTemp int) CheckFunction {
	return func() Event {

		result, err := snmpWalk(ip, community, "1.3.6.1.4.1.4998.1.1.10.1.4.2.1.29", 2*time.Second, 1)

		if err == nil {
			max := 0
			for _, r := range result {
				if r.Value.(int) != 999 && r.Value.(int) > max {
					max = r.Value.(int)
				}
			}
			var state string = "critical"
			if max < maxAllowedTemp {
				state = "ok"
			}
			return Event{Host: host, Service: service, State: state, Metric: float32(max)}
		} else {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
	}
}

func getMaxValueFromSnmpWalk(oid, ip, community string) (uint, error) {
	result, err := snmpWalk(ip, community, oid, 2*time.Second, 1)
	if err == nil {
		max := uint(0)
		for _, r := range result {
			if r.Value.(uint) > max {
				max = r.Value.(uint)
			}
		}
		return max, nil
	} else {
		return 0, err
	}
}

func NewJuniperTempChecker(host, service, ip, community string, maxAllowedTemp uint) CheckFunction {
	return func() Event {
		max, err := getMaxValueFromSnmpWalk("1.3.6.1.4.1.2636.3.1.13.1.7", ip, community)
		if err == nil {
			var state string = "critical"
			if max < maxAllowedTemp {
				state = "ok"
			}
			return Event{Host: host, Service: service, State: state, Metric: float32(max)}
		} else {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
	}
}

func NewJuniperCpuChecker(host, service, ip, community string, maxAllowedTemp uint) CheckFunction {
	return func() Event {
		max, err := getMaxValueFromSnmpWalk("1.3.6.1.4.1.2636.3.1.13.1.8", ip, community)
		if err == nil {
			var state string = "critical"
			if max < maxAllowedTemp {
				state = "ok"
			}
			return Event{Host: host, Service: service, State: state, Metric: float32(max)}
		} else {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
	}
}

func NewRabbitMQQueueLenCheck(host, service, amqpuri, queue string, max int) CheckFunction {
	return func() Event {
		result := Event{Host: host, Service: service}

		conn, err := amqp.Dial(amqpuri)
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}

		ch, err := conn.Channel()
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}
		defer ch.Close()
		defer conn.Close()

		queueInfo, err := ch.QueueInspect(queue)
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}

		var state string = "critical"
		if queueInfo.Messages <= max {
			state = "ok"
		}
		return Event{Host: host, Service: service, State: state, Metric: float32(queueInfo.Messages)}
	}
}

func NewMysqlConnectionCheck(host, service, mysqluri string) CheckFunction {
	return func() Event {
		u, err := url.Parse(mysqluri)
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}

		if u.User == nil {
			return Event{Host: host, Service: service, State: "critical", Description: "No user defined"}
		}
		password, hasPassword := u.User.Password()
		if !hasPassword {
			return Event{Host: host, Service: service, State: "critical", Description: "No password defined"}
		}
		hostAndPort := u.Host
		if !strings.Contains(hostAndPort, ":") {
			hostAndPort = hostAndPort + ":3306"
		}
		var t1 = time.Now()
		con, err := sql.Open("mysql", u.User.Username()+":"+password+"@"+"tcp("+hostAndPort+")"+u.Path)
		defer con.Close()
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
		q := `select CURTIME()`
		row := con.QueryRow(q)
		var date string
		err = row.Scan(&date)
		milliseconds := float32((time.Now().Sub(t1)).Nanoseconds() / 1e6)
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error(), Metric: milliseconds}
		}
		return Event{Host: host, Service: service, State: "ok", Metric: milliseconds}
	}
}

type ObtainMetricFunction func() float32
type CalculateStateFunction func(float32) string

func NewGenericCheck(host, service string, metricFunc ObtainMetricFunction, stateFunc CalculateStateFunction) CheckFunction {
	return func() Event {
		value := metricFunc()
		var state string = stateFunc(value)
		return Event{Host: host, Service: service, State: state, Metric: value}
	}
}
