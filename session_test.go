package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"SolarEdge-Exporter/exporter"
	"github.com/prometheus/client_golang/prometheus"
)

func TestRetryBackoff(t *testing.T) {
	b := retryBackoff{initial: 2 * time.Second, maximum: 30 * time.Second}
	for _, want := range []time.Duration{2, 4, 8, 16, 30, 30, 30} {
		if got := b.next(); got != want*time.Second {
			t.Fatalf("delay = %s, want %s", got, want*time.Second)
		}
	}
	b.delay = 0
	if got := b.next(); got != 2*time.Second {
		t.Fatalf("reset = %s", got)
	}
}

func healthValue(target, name string) float64 {
	families, _ := prometheus.DefaultGatherer.Gather()
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				if l.GetName() == "inverter" && l.GetValue() == target {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}

func TestSessionReinitialization(t *testing.T) {
	// Every stage uses the real Modbus TCP implementation, including strict
	// transaction-ID checks. A bad response must terminate that connection.
	for _, tc := range []struct {
		name               string
		stage              int
		timeout, truncated bool
	}{
		{"startup mismatch", 0, false, false},
		{"detection mismatch", 1, false, false},
		{"inverter mismatch", 2, false, false},
		{"meter mismatch", 3, false, false},
		{"inverter timeout", 2, true, false},
		{"malformed meter", 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			target := listener.Addr().String()
			metrics := []*exporter.MeterMetrics{exporter.NewMeterMetrics(fmt.Sprintf("RecoveryTest%d%t%t", tc.stage, tc.timeout, tc.truncated))}
			serverErrors := make(chan error, 1)
			go func() {
				for session := 0; session < 2; session++ {
					conn, err := listener.Accept()
					if err != nil {
						serverErrors <- err
						return
					}
					err = serveRecoverySession(conn, session == 0, tc.stage, tc.timeout, tc.truncated)
					conn.Close()
					if err != nil {
						serverErrors <- err
						return
					}
				}
				serverErrors <- nil
			}()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				runCollectionWithOptions(ctx, target, metrics, sessionOptions{
					interval: 10 * time.Millisecond, timeout: 50 * time.Millisecond,
					initialBackoff: 50 * time.Millisecond, maxBackoff: 200 * time.Millisecond, clientID: 1,
				})
			}()
			defer cancel()
			deadline := time.Now().Add(3 * time.Second)
			for healthValue(target, "solaredge_collection_success") != 1 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if healthValue(target, "solaredge_collection_success") != 1 {
				t.Fatal("did not recover")
			}
			if healthValue(target, "solaredge_collection_last_success_timestamp_seconds") <= 0 {
				t.Fatal("missing success timestamp")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("collector did not stop")
			}
			select {
			case err := <-serverErrors:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("server did not stop")
			}
		})
	}
}

func serveRecoverySession(conn net.Conn, fail bool, failStage int, timeout, truncated bool) error {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	for stage := 0; ; stage++ {
		var request [12]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			if !fail && (err == io.EOF || err == io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		address := binary.BigEndian.Uint16(request[8:10])
		if stage == 0 && (address != 40000 || binary.BigEndian.Uint16(request[:2]) != 1) {
			return fmt.Errorf("session did not restart with metadata and fresh transaction ID")
		}
		if stage == 1 && address != meterCommonBase {
			return fmt.Errorf("meters not rediscovered")
		}
		quantity := binary.BigEndian.Uint16(request[10:12])
		data := make([]byte, int(quantity)*2)
		if address == meterCommonBase {
			copy(data[4:], "SolarEdge")
		}
		if fail && stage == failStage && timeout {
			// Read without responding; the client must time out and close.
			var extra [1]byte
			_, err := conn.Read(extra[:])
			if err != io.EOF {
				return fmt.Errorf("timeout session not closed: %v", err)
			}
			return nil
		}
		if fail && stage == failStage && truncated {
			data = data[:12]
		}
		response := append([]byte(nil), request[:7]...)
		if fail && stage == failStage && !truncated {
			binary.BigEndian.PutUint16(response[:2], binary.BigEndian.Uint16(request[:2])-1)
		}
		pdu := append([]byte{3, byte(len(data))}, data...)
		binary.BigEndian.PutUint16(response[4:6], uint16(len(pdu)+1))
		if _, err := conn.Write(append(response, pdu...)); err != nil {
			return err
		}
		if fail && stage == failStage {
			var extra [1]byte
			_, err := conn.Read(extra[:])
			if err != io.EOF {
				return fmt.Errorf("bad response accepted or socket not closed: %v", err)
			}
			return nil
		}
	}
}

func TestRetryWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if sleepOrDone(ctx, 30*time.Second) {
		t.Fatal("cancelled retry completed")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation was delayed")
	}
}
