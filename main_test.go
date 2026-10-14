package main

import (
	"errors"
	"testing"

	"SolarEdge-Exporter/exporter"
	"github.com/goburrow/modbus"
)

type readClient struct {
	modbus.Client
	read func(uint16, uint16) ([]byte, error)
}

func (c readClient) ReadHoldingRegisters(address, quantity uint16) ([]byte, error) {
	return c.read(address, quantity)
}

func TestCollectMeters(t *testing.T) {
	metrics := []*exporter.MeterMetrics{
		exporter.NewMeterMetrics("TestMeter1"),
		exporter.NewMeterMetrics("TestMeter2"),
	}
	for _, tc := range []struct {
		name      string
		count     int
		failAt    int
		malformed bool
		want      bool
	}{
		{"no meters", 0, -1, false, true},
		{"all meters succeed", 2, -1, false, true},
		{"first meter fails", 2, 0, false, false},
		{"second meter fails", 2, 1, false, false},
		{"parse failure", 1, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := readClient{read: func(address, quantity uint16) ([]byte, error) {
				index := calls
				calls++
				if address != uint16(meterDataBase+index*meterBlockLength) || quantity != 105 {
					t.Fatalf("unexpected request: %d/%d", address, quantity)
				}
				if index == tc.failAt {
					if tc.malformed {
						return nil, nil
					}
					return nil, errors.New("read timeout")
				}
				return make([]byte, 210), nil
			}}
			if got := collectMeters(client, "test:502", tc.count, metrics); got != tc.want {
				t.Fatalf("success = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectMetersHealth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		err      error
		complete bool
	}{
		{"absent", make([]byte, 130), nil, true},
		{"illegal address means absent", nil, &modbus.ModbusError{ExceptionCode: modbus.ExceptionCodeIllegalDataAddress}, true},
		{"device failure is not absence", nil, &modbus.ModbusError{ExceptionCode: modbus.ExceptionCodeServerDeviceFailure}, false},
		{"timeout", nil, errors.New("timeout"), false},
		{"malformed", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := readClient{read: func(uint16, uint16) ([]byte, error) { return tc.data, tc.err }}
			_, complete := detectMeters(client, "test:502", 1)
			if complete != tc.complete {
				t.Fatalf("detection complete = %v, want %v", complete, tc.complete)
			}
		})
	}
}
