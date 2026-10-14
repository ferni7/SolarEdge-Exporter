package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
)

// Exercise the real TCP client and polling loop: success, then a read failure.
func TestCollectionHealthOnReadFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target := listener.Addr().String()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		for request := 0; request < 3; request++ {
			var frame [12]byte
			if _, err := io.ReadFull(conn, frame[:]); err != nil {
				return
			}
			var pdu []byte
			switch request {
			case 0: // Initial common block.
				pdu = append([]byte{3, 140}, make([]byte, 140)...)
			case 1: // First inverter read succeeds.
				pdu = append([]byte{3, 80}, make([]byte, 80)...)
			case 2: // The next cycle fails with a Modbus exception.
				pdu = []byte{0x83, 4}
			}
			response := append([]byte(nil), frame[:7]...)
			binary.BigEndian.PutUint16(response[4:6], uint16(len(pdu)+1))
			response = append(response, pdu...)
			if _, err := conn.Write(response); err != nil {
				return
			}
		}
	}()

	viper.Set("Exporter.Interval", 1)
	viper.Set("SolarEdge.ClientId", 1)
	defer viper.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCollection(ctx, target, nil)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("collector did not stop")
		}
	}()

	readHealth := func() (float64, float64) {
		t.Helper()
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		success, timestamp := -1.0, -1.0
		for _, family := range families {
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					if label.GetName() != "inverter" || label.GetValue() != target {
						continue
					}
					switch family.GetName() {
					case "solaredge_collection_success":
						success = metric.GetGauge().GetValue()
					case "solaredge_collection_last_success_timestamp_seconds":
						timestamp = metric.GetGauge().GetValue()
					}
				}
			}
		}
		return success, timestamp
	}
	waitHealth := func(want float64) float64 {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if success, timestamp := readHealth(); success == want && timestamp > 0 {
				return timestamp
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("did not observe collection_success = %v", want)
		return 0
	}
	lastSuccess := waitHealth(1)
	if got := waitHealth(0); got != lastSuccess {
		t.Fatalf("failure changed last-success time: %v -> %v", lastSuccess, got)
	}
}
