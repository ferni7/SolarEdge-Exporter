package main

import (
	"context"
	"fmt"
	"time"

	"SolarEdge-Exporter/exporter"
	"SolarEdge-Exporter/solaredge"
	"github.com/goburrow/modbus"
	"github.com/rs/zerolog/log"
)

type sessionOptions struct {
	interval, timeout, initialBackoff, maxBackoff time.Duration
	clientID                                      byte
}

type retryBackoff struct {
	initial, maximum, delay time.Duration
}

func (b *retryBackoff) next() time.Duration {
	if b.delay == 0 {
		b.delay = b.initial
	} else if b.delay >= b.maximum/2 {
		b.delay = b.maximum
	} else {
		b.delay *= 2
	}
	if b.delay > b.maximum {
		b.delay = b.maximum
	}
	return b.delay
}

// Each failed session is discarded, including its transaction counter. The
// transport returns only matching IDs; the library also verifies each reply.
func runCollectionWithOptions(ctx context.Context, target string, metrics []*exporter.MeterMetrics, opts sessionOptions) {
	health := exporter.NewCollectionHealth(target)
	backoff := retryBackoff{initial: opts.initialBackoff, maximum: opts.maxBackoff}
	for ctx.Err() == nil {
		err := collectSession(ctx, target, metrics, opts, health, &backoff)
		if ctx.Err() != nil {
			return
		}
		health.Observe(false, time.Now())
		delay := backoff.next()
		log.Error().Msgf("[%s] Session failed: %v; reinitializing in %s", target, err, delay)
		if !sleepOrDone(ctx, delay) {
			return
		}
	}
}

// collectSession owns exactly one handler/socket. Returning on any error closes
// it before the backoff starts. Startup, detection and all reads share the same
// recovery policy; no failed initialization can fall through into collection.
func collectSession(ctx context.Context, target string, metrics []*exporter.MeterMetrics, opts sessionOptions, health *exporter.CollectionHealth, backoff *retryBackoff) error {
	transport, err := newMatchingTransport(ctx, target, opts.timeout)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer transport.Close()
	packager := modbus.NewTCPClientHandler(target)
	packager.SlaveId = opts.clientID
	client := modbus.NewClient2(packager, transport)
	data, err := client.ReadHoldingRegisters(40000, 70)
	if err != nil {
		return fmt.Errorf("read inverter common block: %w", err)
	}
	common, err := solaredge.NewCommonModel(data)
	if err != nil {
		return fmt.Errorf("parse inverter common block: %w", err)
	}
	log.Info().Msgf("[%s] Inverter Model: %s", target, common.C_Model)
	log.Info().Msgf("[%s] Inverter Serial: %s", target, common.C_SerialNumber)
	log.Info().Msgf("[%s] Inverter Version: %s", target, common.C_Version)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	activeMeters, detected := detectMeters(client, target, len(metrics))
	if !detected {
		return fmt.Errorf("meter detection failed")
	}
	for ctx.Err() == nil {
		data, err := client.ReadHoldingRegisters(40069, 40)
		if err != nil {
			return fmt.Errorf("read inverter registers: %w", err)
		}
		inverter, err := solaredge.NewInverterModel(data)
		if err != nil {
			return fmt.Errorf("parse inverter registers: %w", err)
		}
		setMetrics(inverter, target)
		if !collectMeters(client, target, activeMeters, metrics) {
			return fmt.Errorf("meter collection failed")
		}
		health.Observe(true, time.Now())
		backoff.delay = 0 // Reset only after a complete successful cycle.
		log.Debug().Msgf("[%s] Data retrieved from inverter", target)
		if !sleepOrDone(ctx, opts.interval) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
