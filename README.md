[![Bugs](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=bugs)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)
[![Code Smells](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=code_smells)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)
[![Vulnerabilities](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=vulnerabilities)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)
[![Reliability Rating](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=reliability_rating)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)
[![Security Rating](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=security_rating)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)
[![Maintainability Rating](https://sonarcloud.io/api/project_badges/measure?project=dave92082_SolarEdge-Exporter&metric=sqale_rating)](https://sonarcloud.io/dashboard?id=dave92082_SolarEdge-Exporter)

# SolarEdge Prometheus Exporter

Having just installed a SolarEdge inverter and not happy with the 15 minute delay and low resolution of the monitoring data
provided by the monitoring service/api, I created this exporter to connects directly to SolarEdge inverter over ModBus TCP 
to export (near) real time data to Prometheus.

## Status
The code could use some clean up but I have had it running for a weeks scraping data from the inverter every 5 seconds without any issues.

## Requirements
* SolarEdge Inverter that supports SunSpec protocol (Tested with SE5000 w. CPU version 3.2221.0)
* ModBus TCP Enabled on the inverter
* Local network connection to the inverter (No ZigBee/GSM support)

Modbus TCP is a local network connection only and *does not* interfere or replace your connection to the SolarEdge monitoring 
service. As per the SolarEdge documentation, the two monitoring methods can be used in parallel without impacting each other.

More information on how to enable ModBus TCP can be found in the SolarEdge Documentation [here](https://www.solaredge.com/sites/default/files/sunspec-implementation-technical-note.pdf)

## Quick Start

1. Download the binary from the Releases section for your platform
2. Configure the exporter using *one* of the two methods available.
	
	*Replace the IP address in these samples with the address of your inverter*
	* Environment Variables:
	``` 
		INVERTER_ADDRESS=192.168.1.189
		EXPORTER_INTERVAL=5
		INVERTER_PORT=502
		NUMBER_METERS=1
		LOG_PATH=SolarEdge-Exporter.log
	``` 
	* config.yaml:
	Create a config file named `config.yaml` in the same location that you downloaded the executable with the following contents:
	```yaml
	SolarEdge:
	  InverterAddress: "192.168.1.189"
	  InverterPort: 502
	  # Number of meters to read (0-3). Meters that are not detected are skipped.
	  NumMeters: 1
	Exporter:
	  # Update Interval in seconds
	  Interval: 5
	Log:
	  # Path of the log file
	  Path: "SolarEdge-Exporter.log"
	```
3. Add the target to your prometheus server with port `2112`

### Multiple inverters

`InverterAddress` (or `INVERTER_ADDRESS`) accepts a comma separated list of
addresses, each optionally with its own port (`host:port`, defaulting to
`InverterPort`):

```yaml
SolarEdge:
  InverterAddress: "192.168.1.189,192.168.1.190:1502"
```

All metrics carry an `inverter` label with the `host:port` of the inverter
they were read from, so the data of each inverter can be queried separately,
e.g. `AC_Power{inverter="192.168.1.189:502"}`.

### Meters

Up to three SunSpec meters are supported via `NumMeters` / `NUMBER_METERS`
(default `1`). The first meter is exported with the historical `M_*` metric
prefix, the second and third as `M2_*` and `M3_*`. Meters that are not
installed/enabled on the inverter are detected automatically and skipped, so
no zero-valued meter metrics are exported on systems without a meter.

### Grafana

A sample Grafana dashboard is available in
[grafana/SolarEdgeExporter.json](grafana/SolarEdgeExporter.json).

## Session recovery

On connection, protocol, parsing, or detected-meter read failures, the exporter
closes and discards the entire Modbus session. Each retry uses a fresh handler,
re-reads inverter metadata, and rediscovers enabled meters before polling.
Transaction-ID validation remains strict: only a reply matching the current
request can become a measurement. The transport discards up to three complete
unmatched replies per request, logging each discard, then waits for the matching
reply within the original 10-second deadline. Requests are not resent and stale
replies do not extend the deadline. Exceeding the limit, timing out, or receiving
an invalid frame triggers session reinitialization. Protocol and unit IDs,
frame lengths, and matching-response function codes are still validated.

Consecutive failed attempts wait **2, 4, 8, 16, then 30 seconds**, capped at
30 seconds. Backoff resets only after a complete successful collection cycle,
not merely a successful TCP connection. Polling and retry waits are interruptible
on shutdown; cancellation also closes active sockets and interrupts connection
attempts. Network operations retain their 10-second timeout.
This improves recovery but cannot guarantee recovery from an inverter or
network fault. Use the health metrics below to detect ongoing outages.

## Collection health and stale data

Prometheus `up` only indicates that `/metrics` is reachable, not that inverter
polling works. Two additional metrics carry the same `inverter="host:port"`
label as the measurements:

* `solaredge_collection_success`: `1` when the latest cycle read the inverter,
  completed meter detection, and read all detected meters; `0` on failure or
  before the first completed cycle. An absent/disabled meter is not a failure.
* `solaredge_collection_last_success_timestamp_seconds`: time of the last
  successful complete cycle, or `0` if none has succeeded since startup.
  Failed cycles leave this timestamp unchanged.

Existing measurements remain cached on failures for compatibility. Check these
health metrics before trusting them. They describe complete-cycle health, so a
meter failure can mark collection unhealthy even if inverter values updated.
Check both success and age: during a blocked read, success can still describe
an earlier cycle. Choose an age threshold above your polling interval and
expected read duration (particularly with multiple meters).

Example Prometheus alert rules (adjust thresholds for your configuration):

```yaml
groups:
  - name: solaredge
    rules:
      - alert: SolarEdgeCollectionFailed
        expr: solaredge_collection_success == 0
        for: 30s
        labels:
          severity: warning
        annotations:
          summary: "SolarEdge collection failing for {{ $labels.inverter }}"
      - alert: SolarEdgeDataStale
        expr: time() - solaredge_collection_last_success_timestamp_seconds > 60
        for: 30s
        labels:
          severity: warning
        annotations:
          summary: "SolarEdge data stale for {{ $labels.inverter }}"
      - alert: SolarEdgeExporterDown
        expr: up{job="solaredge-exporter"} == 0
        for: 30s
        labels:
          severity: warning
        annotations:
          summary: "SolarEdge exporter scrape failing"
```

## Metrics

All metrics are labeled with the `inverter` they were read from. In addition
to the inverter metrics below, each detected meter exports the SunSpec meter
values (`M_AC_Current`, `M_AC_Power`, `M_Exported`, `M_Imported`, ... —
prefixed `M_`, `M2_` or `M3_` depending on the meter number).

|		Metric	 	 |	 Type	 |	Description/Help																																	 |
|--------------------|-----------|-------------------------------------------------------------------------------------------------------------------------------------------------------|
|SunSpec_DID     	 | 	 Guage 	 | 	 101 = single phase 102 = split phase1 103 = three phase                                                                                        	 |
|SunSpec_Length  	 | 	 Guage 	 | 	 Registers 50 = Length of model block                                                                                                           	 |
|AC_Current      	 | 	 Guage 	 | 	 Amps AC Total Current value                                                                                                                    	 |
|AC_CurrentA     	 | 	 Guage 	 | 	 Amps AC Phase A Current value                                                                                                                  	 |
|AC_CurrentB     	 | 	 Guage 	 | 	 Amps AC Phase B Current value                                                                                                                  	 |
|AC_CurrentC     	 | 	 Guage 	 | 	 Amps AC Phase C Current value                                                                                                                  	 |
|AC_Current_SF   	 | 	 Guage 	 | 	 AC Current scale factor                                                                                                                        	 |
|AC_VoltageAB    	 | 	 Guage 	 | 	 Volts AC Voltage Phase AB value                                                                                                                	 |
|AC_VoltageBC    	 | 	 Guage 	 | 	 Volts AC Voltage Phase BC value                                                                                                                	 |
|AC_VoltageCA    	 | 	 Guage 	 | 	 Volts AC Voltage Phase CA value                                                                                                                	 |
|AC_VoltageAN    	 | 	 Guage 	 | 	 Volts AC Voltage Phase A to N value                                                                                                            	 |
|AC_VoltageBN    	 | 	 Guage 	 | 	 Volts AC Voltage Phase B to N value                                                                                                            	 |
|AC_VoltageCN    	 | 	 Guage 	 | 	 Volts AC Voltage Phase C to N value                                                                                                            	 |
|AC_Voltage_SF   	 | 	 Guage 	 | 	 AC Voltage scale factor                                                                                                                        	 |
|AC_Power        	 | 	 Guage 	 | 	 Watts AC Power value                                                                                                                           	 |
|AC_Power_SF     	 | 	 Guage 	 | 	 AC Power scale factor                                                                                                                          	 |
|AC_Frequency    	 | 	 Guage 	 | 	 Hertz AC Frequency value                                                                                                                       	 |
|AC_Frequency_SF 	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|AC_VA           	 | 	 Guage 	 | 	 VA Apparent Power                                                                                                                              	 |
|AC_VA_SF        	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|AC_VAR          	 | 	 Guage 	 | 	 VAR Reactive Power                                                                                                                             	 |
|AC_VAR_SF       	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|AC_PF           	 | 	 Guage 	 | 	 % Power Factor                                                                                                                                 	 |
|AC_PF_SF        	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|AC_Energy_WH    	 | 	 Guage 	 | 	 WattHours AC Lifetime Energy production                                                                                                        	 |
|AC_Energy_WH_SF 	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|DC_Current      	 | 	 Guage 	 | 	 Amps DC Current value                                                                                                                          	 |
|DC_Current_SF   	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|DC_Voltage      	 | 	 Guage 	 | 	 Volts DC Voltage value                                                                                                                         	 |
|DC_Voltage_SF   	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|DC_Power        	 | 	 Guage 	 | 	 Watts DC Power value                                                                                                                           	 |
|DC_Power_SF     	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|Temp_Sink       	 | 	 Guage 	 | 	 Degrees C Heat Sink Temperature                                                                                                                	 |
|Temp_SF         	 | 	 Guage 	 | 	 Scale factor                                                                                                                                   	 |
|Status          	 | 	 Guage 	 | 	 Operating State                                                                                                                                	 |
|Status_Vendor   	 | 	 Guage 	 | 	 Vendor-defined operating state and error codes. For error description, meaning and troubleshooting, refer to the SolarEdge Installation Guide. 	 |

