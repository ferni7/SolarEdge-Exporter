package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const maxUnmatchedResponses = 3

// matchingTransport sends each request once, discarding only complete frames
// with a different transaction ID. It never decodes or publishes those frames.
// The existing Modbus packager still validates the returned matching frame.
type matchingTransport struct {
	mu               sync.Mutex
	conn             net.Conn
	timeout          time.Duration
	target           string
	stopCancellation func() bool
}

func newMatchingTransport(ctx context.Context, target string, timeout time.Duration) (*matchingTransport, error) {
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	t := &matchingTransport{conn: conn, timeout: timeout, target: target}
	t.stopCancellation = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return t, nil
}

func (t *matchingTransport) Close() error {
	if t.stopCancellation != nil {
		t.stopCancellation()
	}
	return t.conn.Close()
}

func (t *matchingTransport) Send(request []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(request) < 8 {
		return nil, fmt.Errorf("invalid Modbus request length: %d", len(request))
	}
	if t.timeout <= 0 {
		return nil, fmt.Errorf("Modbus response timeout must be positive")
	}
	// One deadline covers the write and every discarded response. Receiving
	// stale frames must not extend the time allowed for the matching response.
	if err := t.conn.SetDeadline(time.Now().Add(t.timeout)); err != nil {
		return nil, err
	}
	for remaining := request; len(remaining) > 0; {
		n, err := t.conn.Write(remaining)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	wanted := binary.BigEndian.Uint16(request[:2])
	for discarded := 0; ; {
		var header [7]byte
		if _, err := io.ReadFull(t.conn, header[:]); err != nil {
			return nil, err
		}
		length := int(binary.BigEndian.Uint16(header[4:6]))
		if length < 2 || length > 254 {
			return nil, fmt.Errorf("invalid Modbus response length: %d", length)
		}
		if binary.BigEndian.Uint16(header[2:4]) != binary.BigEndian.Uint16(request[2:4]) {
			return nil, fmt.Errorf("Modbus response protocol ID mismatch")
		}
		if header[6] != request[6] {
			return nil, fmt.Errorf("Modbus response unit ID mismatch")
		}
		frame := make([]byte, 6+length)
		copy(frame, header[:])
		if _, err := io.ReadFull(t.conn, frame[7:]); err != nil {
			return nil, err
		}
		got := binary.BigEndian.Uint16(header[:2])
		if got == wanted {
			if frame[7] != request[7] && frame[7] != request[7]|0x80 {
				return nil, fmt.Errorf("Modbus response function code mismatch")
			}
			return frame, nil
		}
		if discarded == maxUnmatchedResponses {
			return nil, fmt.Errorf("Modbus unmatched response limit exceeded: response transaction ID %d, expected %d", got, wanted)
		}
		discarded++
		log.Warn().Msgf("[%s] Discarding unmatched Modbus response transaction ID %d (expected %d, %d/%d)", t.target, got, wanted, discarded, maxUnmatchedResponses)
	}
}
