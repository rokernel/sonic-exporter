# sonic-exporter

Prometheus exporter for [SONiC](https://github.com/sonic-net/SONiC) NOS.

Currently supported collectors:
- [HW collector](internal/collector/hw_collector.go): collects metrics about PSU and Fan operation
- [Interface collector](internal/collector/interface_collector.go): collect metrics about interface operation and performance.
- [CRM collector](internal/collector/crm_collector.go): collects Critial Resource Monitoring metrics.
- [Queue collector](internal/collector/queue_collector.go): collects metrics about queues.
- [LLDP collector](internal/collector/lldp_collector.go): collects LLDP neighbor information from SONiC Redis.
- [VLAN collector](internal/collector/vlan_collector.go): collects VLAN and VLAN member state from SONiC Redis.
- [LAG collector](internal/collector/lag_collector.go): collects PortChannel and member state from SONiC Redis.
- [FDB collector](internal/collector/fdb_collector.go): collects FDB summary metrics from SONiC ASIC DB.
- [System collector](internal/collector/system_collector.go): experimental collector for switch identity, software metadata, and uptime using read-only sources (disabled by default).

# Usage

1. Run binary 
```bash
$ ./sonic-exporter
```

2. You can verify that exporter is running by cURLing the `/metrics` endpoint. 
```bash
$ curl localhost:9101/metrics
```

# Configuration

Environment variables:

- `REDIS_ADDRESS` - redis connection string, if using unix socket set `REDIS_NETWORK` to `unix`. Default: `localhost:6379`.
- `REDIS_PASSWORD` - password used when connecting to redis.
- `REDIS_NETWORK` - redis network type, either tcp or unix. Default: `tcp`.
- `LLDP_ENABLED` - enable LLDP collector. Default: `true`.
- `LLDP_INCLUDE_MGMT` - include management interface LLDP entries (for example `eth0`). Default: `true`.
- `LLDP_REFRESH_INTERVAL` - LLDP cache refresh interval. Default: `30s`.
- `LLDP_TIMEOUT` - timeout for one LLDP refresh cycle. Default: `2s`.
- `LLDP_MAX_NEIGHBORS` - maximum number of LLDP neighbors exported per refresh. Default: `512`.
- `VLAN_ENABLED` - enable VLAN collector. Default: `true`.
- `VLAN_REFRESH_INTERVAL` - VLAN cache refresh interval. Default: `30s`.
- `VLAN_TIMEOUT` - timeout for one VLAN refresh cycle. Default: `2s`.
- `VLAN_MAX_VLANS` - maximum number of VLANs exported per refresh. Default: `1024`.
- `VLAN_MAX_MEMBERS` - maximum number of VLAN members exported per refresh. Default: `8192`.
- `LAG_ENABLED` - enable LAG collector. Default: `true`.
- `LAG_REFRESH_INTERVAL` - LAG cache refresh interval. Default: `30s`.
- `LAG_TIMEOUT` - timeout for one LAG refresh cycle. Default: `2s`.
- `LAG_MAX_LAGS` - maximum number of LAGs exported per refresh. Default: `512`.
- `LAG_MAX_MEMBERS` - maximum number of LAG members exported per refresh. Default: `4096`.
- `FDB_ENABLED` - enable FDB collector. Default: `false`.
- `FDB_REFRESH_INTERVAL` - FDB cache refresh interval. Default: `60s`.
- `FDB_TIMEOUT` - timeout for one FDB refresh cycle. Default: `2s`.
- `FDB_MAX_ENTRIES` - maximum number of ASIC FDB entries processed per refresh. Default: `50000`.
- `FDB_MAX_PORTS` - maximum number of per-port FDB series exported. Default: `1024`.
- `FDB_MAX_VLANS` - maximum number of per-VLAN FDB series exported. Default: `4096`.
- `SYSTEM_ENABLED` - enable system metadata collector (experimental). Default: `false`.
- `SYSTEM_REFRESH_INTERVAL` - system metadata cache refresh interval. Default: `60s`.
- `SYSTEM_TIMEOUT` - timeout for one system metadata refresh cycle. Default: `4s`.
- `SYSTEM_COMMAND_ENABLED` - allow read-only command fallbacks (`show platform summary`, `show version`, `show platform syseeprom`). Default: `true`.
- `SYSTEM_COMMAND_TIMEOUT` - timeout for one allowed command execution. Default: `2s`.
- `SYSTEM_COMMAND_MAX_OUTPUT_BYTES` - max output bytes read from one command. Default: `262144`.
- `SYSTEM_VERSION_FILE` - path to SONiC version metadata file. Default: `/etc/sonic/sonic_version.yml`.
- `SYSTEM_MACHINE_CONF_FILE` - path to machine config file. Default: `/host/machine.conf`.
- `SYSTEM_HOSTNAME_FILE` - path to hostname file. Default: `/etc/hostname`.
- `SYSTEM_UPTIME_FILE` - path to uptime file. Default: `/proc/uptime`.

## System Collector (Experimental)

The `system_collector` is currently experimental and is disabled by default for stability.

To enable it:
```bash
$ SYSTEM_ENABLED=true ./sonic-exporter
```

What this collector exports:

- `sonic_system_identity_info` - hostname, platform, hwsku, asic, asic_count, serial, model, revision.
- `sonic_system_software_info` - SONiC version, OS version, Debian, kernel, build metadata.
- `sonic_system_uptime_seconds` - uptime in seconds.
- `sonic_system_collector_success`, `sonic_system_scrape_duration_seconds`, `sonic_system_cache_age_seconds`.

Data sources and fallback order:

1. Redis first (`DEVICE_METADATA|localhost`, `CHASSIS_INFO|chassis 1`)
2. Local read-only files (`/etc/sonic/sonic_version.yml`, `/host/machine.conf`, `/etc/hostname`, `/proc/uptime`)
3. Optional read-only command fallback (if `SYSTEM_COMMAND_ENABLED=true`):
   - `show platform summary --json`
   - `show version`
   - `show platform syseeprom`

Read-only and safety behavior:

- No Redis writes and no file writes.
- Command execution is allowlisted.
- Command timeout and output size limits are enforced.
- Missing fields become `unknown` instead of failing scrapes.
- Metadata refresh is cached, so `/metrics` stays responsive.

Debug mode behavior (`--log.level=debug`):

- Logs when fields are missing but expected.
- Logs which data source populated each field.
- Logs when fallback sources are skipped because a higher-priority source already set the field.

## Validated Platforms

The exporter has been validated on the following platforms:

These tests were done with SONiC Community versions, not SONiC Enterprise versions.

| Model Number | SONiC Software Version | SONiC OS Version | Distribution | Kernel | Platform | ASIC |
|---|---|---|---|---|---|---|
| DellEMC-S5232f-C8D48 | 202012 | 10 | Debian 10.13 | 4.19.0-12-2-amd64 | x86_64-dellemc_s5232f_c3538-r0 | broadcom |
| SSE-T7132SR | 202505 | 12 | Debian 12.11 | 6.1.0-29-2-amd64 | x86_64-supermicro_sse_t7132s-r0 | marvell-teralynx |
| MSN2100-CB2FC | 202411 | 12 | Debian 12.12 | 6.1.0-29-2-amd64 | x86_64-mlnx_msn2100-r0 | mellanox |

## Collector Examples (Anonymized, Compact)

These examples are synthetic and anonymized. Use them as query patterns. Labels can vary by SONiC platform/version.

- **Interface collector** - interface health and traffic
  - `sonic_interface_operational_status{device="Ethernet0"} 1`
  - `sonic_interface_receive_bytes_total{device="Ethernet0"} 4.8e+09`
  - Query: `rate(sonic_interface_receive_bytes_total[5m])`

- **HW collector** - PSU and fan status
  - `sonic_hw_psu_operational_status{psu="PSU1"} 1`
  - `sonic_hw_fan_speed_rpm{fan="FAN3"} 14200`
  - Query: `sonic_hw_psu_operational_status == 0`

- **CRM collector** - resource usage and headroom
  - `sonic_crm_stats_used{resource="ipv4_route"} 1610`
  - `sonic_crm_stats_available{resource="ipv4_route"} 80299`
  - Query: `100 * sonic_crm_stats_used / (sonic_crm_stats_used + sonic_crm_stats_available)`

- **Queue collector** - queue drops and watermark pressure
  - `sonic_queue_dropped_packets_total{device="Ethernet0",queue="3"} 73`
  - `sonic_queue_watermark_bytes_total{device="Ethernet0",queue="3",type="periodic",watermark="queue_stat_periodic"} 44`
  - Query: `rate(sonic_queue_dropped_packets_total[5m])`

- **LLDP collector** - neighbor discovery and cache health
  - `sonic_lldp_neighbor_info{local_interface="Ethernet88",local_role="frontpanel",remote_system_name="leaf02.example.net",remote_port_id="Ethernet88",remote_chassis_id="00:11:22:33:44:55",remote_mgmt_ip="192.0.2.20"} 1`
  - `sonic_lldp_neighbors 64`
  - Query: `sonic_lldp_neighbors`

- **VLAN collector** - VLAN state and member mapping
  - `sonic_vlan_admin_status{vlan="Vlan1000"} 1`
  - `sonic_vlan_member_info{vlan="Vlan1000",member="Ethernet0",tagging_mode="untagged"} 1`
  - Query: `sonic_vlan_members`

- **LAG collector** - PortChannel status and member state
  - `sonic_lag_oper_status{lag="PortChannel1"} 1`
  - `sonic_lag_member_status{lag="PortChannel1",member="Ethernet24"} 1`
  - Query: `sonic_lag_oper_status == 0`

- **FDB collector** - MAC learning scale and distribution
  - `sonic_fdb_entries 1331`
  - `sonic_fdb_entries_by_port{port="Ethernet88"} 214`
  - Query: `topk(10, sonic_fdb_entries_by_port)`

- **System collector (experimental, when enabled)** - switch identity and software metadata
  - `sonic_system_identity_info{hostname="switch01.example.net",platform="x86_64-vendor_switch-r0",hwsku="Example-SKU",asic="broadcom",asic_count="1",serial="ABC123456",model="Model-X",revision="A01"} 1`
  - `sonic_system_software_info{sonic_version="SONiC.202012.example",debian_version="10.13",kernel_version="4.19.0-12-2-amd64",build_commit="193959ba2"} 1`
  - Query: `sonic_system_uptime_seconds`

- **node_exporter collectors** - host CPU, memory, filesystem
  - `node_cpu_seconds_total{cpu="0",mode="idle"} 5.93e+06`
  - `node_memory_MemAvailable_bytes 1.24e+10`
  - Query: `100 - (avg by(instance) (irate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`

# Development

1. Development environment is based on docker-compose. To start it run:
```bash
$ docker-compose up --build -d
```

2. To verify that development environment is ready try cURLing the `/metrics` endpoint, you should see exported metrics.:
```bash
$ curl localhost:9101/metrics
```

3. After making code changes rebuild docker container:
```bash
$ docker-compose down
$ docker-compose up --build -d
```

4. To build a local binary:
```bash
$ ./scripts/build.sh
```

This script builds a static Linux binary (`CGO_ENABLED=0`), which is safer for older SONiC images.

Optional cross-build example:
```bash
$ TARGET_OS=linux TARGET_ARCH=amd64 ./scripts/build.sh
```

5. To build a release tarball (binary + sha256):
```bash
$ ./scripts/package.sh
```

Optional package version override:
```bash
$ VERSION=v0.1.0 ./scripts/package.sh
```

In case you need to add additional keys to redis database don't forget to run `SAVE` in redis after doing so:
```bash
$ redis-cli
$ 127.0.0.1:6379> SAVE
```

## Test

Currently, tests are using mockredis database which is populated from [fixture files](fixtures/test/).
To run tests manually simply execute:
```bash
$ go test -v ./... 
```
