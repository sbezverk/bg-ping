package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

type pingPacket struct {
	ID  int
	Seq int
}

var outage bool

func isValidIPv4(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) < 4 {
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

func pingServer(c *icmp.PacketConn, targetAddr string, control chan pingPacket, logFile *os.File) {
	b := make([]byte, 65507)
	for {
		count, packetAddr, err := c.ReadFrom(b)
		if err != nil {
			recordEvent(fmt.Sprintf("pingServer failed to read icmp packet: %v", err), logFile)
			continue
		}
		if strings.Compare(packetAddr.String(), targetAddr) != 0 {
			// Receied uninteresting packet, just ignoring it...
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
			// log.Printf("Echo reply packet: ID %d Seq %d", b.ID, b.Seq)
			control <- pingPacket{
				ID:  b.ID,
				Seq: b.Seq,
			}
		}
	}
}

func timeStamp() string {
	t := time.Now()
	return fmt.Sprintf("%d-%02d-%02dT%02d:%02d:%02d_%04d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000000)
}

func recordEvent(msg string, logFile *os.File) {
	r := fmt.Sprintf("| %-30s| %-26s|\n", msg, timeStamp())
	if _, err := logFile.WriteString(r); err != nil {
		log.Fatalf("Failed to record event into the log: %v\n", err)
		os.Exit(1)
	}
	logFile.Sync()
}

func pingClient(c *icmp.PacketConn, targetAddr string, control chan pingPacket, logFile *os.File) {

	processID := rand.Intn(65535)
	processSeq := 1
	for {
		wm := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 8,
			Body: &icmp.Echo{
				ID:   processID,
				Seq:  processSeq,
				Data: []byte("12345677890"),
			},
		}
		wb, err := wm.Marshal(nil)
		if err != nil {
			recordEvent(fmt.Sprintf("pingClient failed to marshal icmp packet: %v\n", err), logFile)
		}
		_, err = c.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(targetAddr)})
		if err != nil {
			recordEvent(fmt.Sprintf("pingClient failed to send a packet: %v\n", err), logFile)
		}
		select {
		case p := <-control:
			if p.ID == processID && p.Seq == processSeq {
				if outage {
					recordEvent("Connectivity outage cleared", logFile)
				}
				outage = false
				processSeq++
				time.Sleep(900 * time.Millisecond)
			}
		case <-time.After(1900 * time.Millisecond):
			if !outage {
				recordEvent("Connectivity outage detected", logFile)
			}
			outage = true
		}
	}
}

func startLogging(ip string) *os.File {
	t := time.Now()
	logFileName := fmt.Sprintf("/tmp/bg-ping_%s_%d-%02d-%02dT%02d:%02d:%02d", ip, t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
	logFile, err := os.Create(logFileName)
	if err != nil {
		log.Fatalf("%s failed to create log %s: %v\n", os.Args[0], logFileName, err)
		os.Exit(1)
	}
	return logFile
}

func main() {

	if len(os.Args) < 2 {
		log.Fatalf("%s missing remote ip address for ping, exiting...", os.Args[0])
		os.Exit(1)
	}
	if !isValidIPv4(os.Args[1]) {
		log.Fatalf("%s invalid %s is an invalid ip address, exiting...", os.Args[0], os.Args[1])
		os.Exit(1)
	}
	logFile := startLogging(os.Args[1])
	defer logFile.Close()

	connection, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatalf("%s failed to listen for icmp packets with: %v, exiting...", os.Args[0], err)
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
			fmt.Fprintf(logFile, "%s: Captured %v, closing log and terminating...\n", timeStamp(), sig)
			connection.Close()
			logFile.Close()
			os.Exit(0)
		}
	}()

	fmt.Fprintf(logFile, "%s: Starting pingServer and pingClient checking connectivity to %s .\n", timeStamp(), os.Args[1])
	outage = false
	control := make(chan pingPacket)
	go pingServer(connection, os.Args[1], control, logFile)
	pingClient(connection, os.Args[1], control, logFile)
}
