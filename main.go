package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

type pingPacket struct {
	ID  int
	Seq int
}

type pClient struct {
	control chan pingPacket
	ID      int
	ip      string
	outage  bool
}

var wg sync.WaitGroup

const programVersion = "0.2.1"

func usage() string {
	return fmt.Sprintf("\nUsage:\n%s \t--ip Comma separated list of IPs to monitor, ex: --ip X.X.X.X,Y.Y.Y.Y \n\t\t[--log folder where to create the log file. Default: /var/log/ ]\n\n", os.Args[0])

}

func isValidIPv4(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	for _, x := range parts {
		if i, err := strconv.Atoi(x); err == nil {
			if i < 0 || i > 255 {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

func timeStamp() string {
	t := time.Now()
	return fmt.Sprintf("%d-%02d-%02dT%02d:%02d:%02d_%04d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000000)
}

func startLogging(logLocation string) *os.File {
	logFileName := fmt.Sprintf("%s/bg-ping.log", logLocation)
	logFile, err := os.Create(logFileName)
	if err != nil {
		log.Fatalf("%s failed to create log %s: %v\n", os.Args[0], logFileName, err)
		os.Exit(1)
	}
	return logFile
}

func parseIPs(listIPs string) ([]string, error) {
	var pingIPs []string
	ips := strings.Split(listIPs, ",")
	for _, ip := range ips {
		if !isValidIPv4(ip) {
			return nil, fmt.Errorf(" %s is an invalid ip address", ip)
		}
		pingIPs = append(pingIPs, ip)
	}
	return pingIPs, nil
}

func recordEvent(msg string, logFile *os.File) {
	r := fmt.Sprintf("| %-80s| %-26s|\n", msg, timeStamp())
	if _, err := logFile.WriteString(r); err != nil {
		log.Fatalf("Failed to record event into the log: %v\n", err)
		os.Exit(1)
	}
	logFile.Sync()
}

func pingServer(c *icmp.PacketConn, clients map[int]pClient, logFile *os.File) {
	b := make([]byte, 65507)
	for {
		count, _, err := c.ReadFrom(b)
		if err != nil {
			recordEvent(fmt.Sprintf("pingServer failed to read icmp packet: %v", err), logFile)
			continue
		}

		// log.Printf("pingServer received %d bytes from ip: %s ", count, packetAddr.String())
		m, err := icmp.ParseMessage(1, b[:count])
		if err != nil {
			recordEvent(fmt.Sprintf("pingServer failed to parse icmp packet: %v", err), logFile)
			continue
		}
		switch b := m.Body.(type) {
		case *icmp.Echo:
			if _, ok := clients[b.ID]; ok {
				// log.Printf("Sending to client ID: %d channel: %v\n", b.ID, clients[b.ID].control)
				clients[b.ID].control <- pingPacket{
					ID:  b.ID,
					Seq: b.Seq,
				}
			}
		}
	}
}

func pingClient(c *icmp.PacketConn, clientID int, client pClient, logFile *os.File) {
	processSeq := 1
	for {
		wm := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 8,
			Body: &icmp.Echo{
				ID:   clientID,
				Seq:  processSeq,
				Data: []byte("12345677890"),
			},
		}
		wb, err := wm.Marshal(nil)
		if err != nil {
			recordEvent(fmt.Sprintf("pingClient: failed to marshal icmp packet to: %s with: %v", client.ip, err), logFile)
		}
		_, err = c.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(client.ip)})
		if err != nil {
			recordEvent(fmt.Sprintf("pingClient: failed to send a packet to: %s %v", client.ip, err), logFile)
		}
		select {
		case p := <-client.control:
			if p.ID == clientID {
				if client.outage {
					recordEvent(fmt.Sprintf("pingClient: Connectivity outage cleared for: %s", client.ip), logFile)
				}
				client.outage = false
			}
		case <-time.After(1900 * time.Millisecond):
			if !client.outage {
				recordEvent(fmt.Sprintf("pingClient: Connectivity outage detected for: %s", client.ip), logFile)
			}
			client.outage = true
		}
		processSeq++
		time.Sleep(900 * time.Millisecond)
	}
}

func main() {

	listIPs := flag.String("ip", "", "comma seprated list of ip addresses to monitor.")
	logLocation := flag.String("log", "/var/log/", "Location of the log file.")
	help := flag.Bool("help", false, "Prints usage.")
	version := flag.Bool("ver", false, "Prints the program's version")

	flag.Parse()

	if *help {
		fmt.Printf("%s", usage())
		os.Exit(0)
	}
	if *version {
		fmt.Printf("\nVersion: %s\n\n", programVersion)
		os.Exit(0)
	}
	if len(flag.Args()) != 0 {
		fmt.Printf("\nUnknown parameter %s see usage below, terinating.\n", flag.Args())
		fmt.Printf("%s", usage())
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		log.Fatalf("%s missing remote ip address(es) for ping, terminating...", os.Args[0])
		os.Exit(1)
	}

	// Parse and validate the list of IPs passed as argument(s)
	pingIPs, err := parseIPs(*listIPs)
	if err != nil {
		log.Fatalf("%s failed: %v, terminating...", os.Args[0], err)
		os.Exit(1)
	}

	// Start logging
	logFile := startLogging(*logLocation)
	defer logFile.Close()

	// Build pingClientsList
	pingClientList := map[int]pClient{}
	for id, ip := range pingIPs {
		pingClientList[id+1] = pClient{
			control: make(chan pingPacket),
			ID:      id + 1,
			ip:      ip,
			outage:  false,
		}
	}

	// Open connection for listen all incoming icmp packets
	connection, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		recordEvent(fmt.Sprintf("%s failed to listen for icmp packets with: %v, terminating", os.Args[0], err), logFile)
		os.Exit(1)
	}
	defer connection.Close()

	// Capture signals to close the log file before exiting
	c := make(chan os.Signal, 1)
	signal.Notify(c,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	go func() {
		for sig := range c {
			recordEvent(fmt.Sprintf("Captured %v, closing log and terminating", sig), logFile)
			connection.Close()
			logFile.Close()
			os.Exit(0)
		}
	}()

	recordEvent(fmt.Sprintf("Starting pingServer and pingClient"), logFile)

	// Starting pingServer and passing list of all ping clients with their
	// corresponding information
	go pingServer(connection, pingClientList, logFile)

	// Adding wait groups just for main to wait on something other than dead loop
	// this programm does not have a way to terminate other than kill.
	wg.Add(len(pingClientList))
	for id := range pingClientList {
		go pingClient(connection, id, pingClientList[id], logFile)
	}
	wg.Wait()
}
