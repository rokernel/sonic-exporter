# Architecture

This document explains how `sonic-exporter` is structured and how metrics flow from SONiC data sources to Prometheus.

## High-level design

```mermaid
flowchart TD
    A[SONiC data sources] --> B[Collector refresh loops]
    B --> C[In-memory cache]
    C --> D[/metrics handler]
    D --> E[Prometheus scrape]

    A1[(CONFIG_DB)] --> A
    A2[(STATE_DB)] --> A
    A3[(COUNTERS_DB)] --> A
    A4[(ASIC_DB)] --> A
    A5[/Read-only local files/] --> A
    A6[[Allowlisted commands]] --> A
```

## Runtime wiring

- Entrypoint: `cmd/sonic-exporter/main.go`
- SONiC collectors are created with `NewXCollector(...)` constructors.
- Optional collectors are registered only when `IsEnabled()` returns true.
- A curated `node_exporter` subset is registered in the same binary.

## Collector inventory

All collectors are in `internal/collector/`:

- `interface_collector.go`
- `hw_collector.go`
- `crm_collector.go`
- `queue_collector.go`
- `lldp_collector.go`
- `vlan_collector.go`
- `lag_collector.go`
- `fdb_collector.go`
- `system_collector.go` (experimental)
- `docker_collector.go` (experimental)

## Source and safety model

- Collectors are read-only by design.
- Redis access is centralized in `pkg/redis/client.go`.
- Experimental collectors are opt-in (`SYSTEM_ENABLED`, `DOCKER_ENABLED`).
- Timeouts, maximum item caps, and cache refresh intervals are configurable through env vars.

## Project layout

```text
sonic-exporter/
├── cmd/sonic-exporter/      # process bootstrap and collector registration
├── internal/collector/      # collector implementations + tests
├── pkg/redis/               # Redis client wrapper used by collectors
├── fixtures/test/           # miniredis fixtures for tests
├── scripts/                 # static build and package scripts
└── .github/workflows/       # CI workflows
```

## Build and test

```bash
go test ./...
go build ./...
./scripts/build.sh
./scripts/package.sh
docker-compose up --build -d
```
