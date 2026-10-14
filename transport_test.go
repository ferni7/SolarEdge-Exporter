package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goburrow/modbus"
)

func responseFrame(id uint16, value uint16) []byte {
	frame := []byte{0, 0, 0, 0, 0, 5, 1, 3, 2, 0, 0}
	binary.BigEndian.PutUint16(frame[:2], id)
	binary.BigEndian.PutUint16(frame[9:], value)
	return frame
}

func TestMatchingTransport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		frames    [][]byte
		wantError string
	}{
		{"matching", [][]byte{responseFrame(1, 42)}, ""},
		{"previous session then matching", [][]byte{responseFrame(231, 99), responseFrame(1, 42)}, ""},
		{"three stale then matching", [][]byte{responseFrame(231, 99), responseFrame(0, 99), responseFrame(0, 99), responseFrame(1, 42)}, ""},
		{"limit exceeded", [][]byte{responseFrame(0, 99), responseFrame(0, 99), responseFrame(0, 99), responseFrame(0, 99), responseFrame(1, 42)}, "limit exceeded"},
		{"wrong unit", [][]byte{{0, 1, 0, 0, 0, 5, 2, 3, 2, 0, 42}}, "unit ID mismatch"},
		{"wrong protocol", [][]byte{{0, 1, 0, 1, 0, 5, 1, 3, 2, 0, 42}}, "protocol ID mismatch"},
		{"zero length", [][]byte{{0, 1, 0, 0, 0, 0, 1}}, "response length"},
		{"oversized", [][]byte{{0, 1, 0, 0, 1, 0, 1}}, "response length"},
		{"truncated", [][]byte{{0, 1, 0, 0, 0, 5, 1, 3}}, "EOF"},
		{"wrong function still rejected", [][]byte{{0, 1, 0, 0, 0, 5, 1, 4, 2, 0, 42}}, "function code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			go func() {
				defer serverConn.Close()
				var request [12]byte
				if _, err := io.ReadFull(serverConn, request[:]); err != nil {
					return
				}
				// Coalesced replies exercise framing rather than packet boundaries.
				var all []byte
				for _, f := range tc.frames {
					all = append(all, f...)
				}
				_, _ = serverConn.Write(all)
			}()
			transport := &matchingTransport{conn: clientConn, timeout: time.Second, target: "test"}
			packager := modbus.NewTCPClientHandler("unused")
			packager.SlaveId = 1
			data, err := modbus.NewClient2(packager, transport).ReadHoldingRegisters(40069, 1)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != 2 || binary.BigEndian.Uint16(data) != 42 {
				t.Fatalf("wrong response published: %x", data)
			}
		})
	}
}

func TestDuplicateThenMatchingSameSession(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		defer serverConn.Close()
		for id := uint16(1); id <= 2; id++ {
			var request [12]byte
			if _, err := io.ReadFull(serverConn, request[:]); err != nil {
				return
			}
			if id == 2 {
				// Exact captured pattern: previous reply again, then current reply.
				if _, err := serverConn.Write(responseFrame(1, 10)); err != nil {
					return
				}
			}
			frame := responseFrame(id, id*10)
			// Fragment a frame across writes to exercise io.ReadFull.
			for _, fragment := range [][]byte{frame[:3], frame[3:7], frame[7:]} {
				if _, err := serverConn.Write(fragment); err != nil {
					return
				}
			}
		}
	}()
	packager := modbus.NewTCPClientHandler("unused")
	packager.SlaveId = 1
	client := modbus.NewClient2(packager, &matchingTransport{conn: clientConn, timeout: time.Second, target: "test"})
	for _, want := range []uint16{10, 20} {
		data, err := client.ReadHoldingRegisters(40069, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.BigEndian.Uint16(data); got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	}
}

func TestStaleFramesDoNotExtendDeadline(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		defer serverConn.Close()
		var request [12]byte
		if _, err := io.ReadFull(serverConn, request[:]); err != nil {
			return
		}
		for i := 0; i < 3; i++ {
			time.Sleep(20 * time.Millisecond)
			if _, err := serverConn.Write(responseFrame(0, 99)); err != nil {
				return
			}
		}
		var extra [1]byte
		_, _ = serverConn.Read(extra[:])
	}()
	packager := modbus.NewTCPClientHandler("unused")
	packager.SlaveId = 1
	client := modbus.NewClient2(packager, &matchingTransport{conn: clientConn, timeout: 100 * time.Millisecond, target: "test"})
	start := time.Now()
	_, err := client.ReadHoldingRegisters(40069, 1)
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("deadline extended: %s", elapsed)
	}
}

func TestTransportCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request [12]byte
		_, _ = io.ReadFull(conn, request[:])
		cancel()
	}()
	transport, err := newMatchingTransport(ctx, listener.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	packager := modbus.NewTCPClientHandler("unused")
	packager.SlaveId = 1
	start := time.Now()
	_, err = modbus.NewClient2(packager, transport).ReadHoldingRegisters(40069, 1)
	if err == nil {
		t.Fatal("cancelled read succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation did not interrupt read")
	}
}
