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

## Validated Platforms

The exporter has been validated on the following platforms:

These tests were done with SONiC Community versions, not SONiC Enterprise versions.

| Model Number | SONiC Software Version | SONiC OS Version | Distribution | Kernel | Platform | ASIC |
|---|---|---|---|---|---|---|
| DellEMC-S5232f-C8D48 | 202012 | 10 | Debian 10.13 | 4.19.0-12-2-amd64 | x86_64-dellemc_s5232f_c3538-r0 | broadcom |
| SSE-T7132SR | 202505 | 12 | Debian 12.11 | 6.1.0-29-2-amd64 | x86_64-supermicro_sse_t7132s-r0 | marvell-teralynx |
| MSN2100-CB2FC | 202411 | 12 | Debian 12.12 | 6.1.0-29-2-amd64 | x86_64-mlnx_msn2100-r0 | mellanox |

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
