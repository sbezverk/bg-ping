package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
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

func pingServer(c *icmp.PacketConn, targetAddr string, control chan pingPacket) {
	b := make([]byte, 65507)
	for {

		count, packetAddr, err := c.ReadFrom(b)
		if err != nil {
			// log.Printf("pingServer failed to read icmp packet: %v", err)
			continue
		}
		if strings.Compare(packetAddr.String(), targetAddr) != 0 {
			// Receied uninteresting packet, just ignoring it...
			continue
		}
		// log.Printf("pingServer received %d bytes from ip: %s ", count, packetAddr.String())
		m, err := icmp.ParseMessage(1, b[:count])
		if err != nil {
			// log.Printf("pingServer failed to parse icmp packet: %v", err)
			continue
		}
		switch b := m.Body.(type) {
		case *icmp.Echo:
			// log.Printf("Echo reply packet: ID %d Seq %d", b.ID, b.Seq)
			r := pingPacket{
				ID:  b.ID,
				Seq: b.Seq,
			}
			control <- r
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
			log.Fatalf("pingClient failed to marshal icmp packet: %v\n", err)
		}
		_, err = c.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(targetAddr)})
		if err != nil {
			log.Fatalf("pingClient failed to send a packet: %v\n", err)
		}
		select {
		case p := <-control:
			// log.Printf("reply received ID %d Seq %d\n", p.ID, p.Seq)
			if p.ID == processID && p.Seq == processSeq {
				if outage {
					recordEvent("Connectivity outage cleared", logFile)
				}
				outage = false
				processSeq++
				time.Sleep(900 * time.Millisecond)
			}
		case <-time.After(1900 * time.Millisecond):
			// log.Printf("Pause longer than 2 seconds, connectivity outage...")
			if !outage {
				recordEvent("Connectivity outage detected", logFile)
			}
			outage = true
		}
	}
}

func main() {

	if len(os.Args) < 2 {
		log.Fatalf("%s missing remote ip address for ping, exiting...", os.Args[0])
	}

	// Let's do some pining
	connection, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatalf("%s failed to listen for icmp packets with: %v, exiting...", os.Args[0], err)
	}
	defer connection.Close()
	t := time.Now()
	logFileName := fmt.Sprintf("/tmp/bg-ping_%s_%d-%02d-%02dT%02d:%02d:%02d", os.Args[1], t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
	logFile, err := os.Create(logFileName)
	if err != nil {
		log.Fatalf("%s failed to create log %s: %v\n", os.Args[0], logFileName, err)
	}
	// defer logFile.Close()

	// Capture SIGTERM to close the log file before exiting
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
	// log.Printf("Listening for icmp on: %s\n", connection.LocalAddr().String())
	outage = false
	control := make(chan pingPacket)
	go pingServer(connection, os.Args[1], control)
	pingClient(connection, os.Args[1], control, logFile)
}
