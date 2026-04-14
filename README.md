# Traffic Monitor

Traffic Monitor is a standalone Go service for collecting LAN traffic usage from Mihomo.

It polls Mihomo `/connections`, stores traffic deltas in SQLite, and serves a built-in web UI from the same binary.

## Features

- Stable HTTP polling
- SQLite persistence
- Built-in single-page web UI
- Aggregation by source IP, host, process, and outbound
- Trend API and history cleanup API
- Docker-friendly deployment

## Embedded UI

The `web/` directory is embedded into the executable with Go `embed`.

That means:

- the compiled binary already contains the web page
- Docker images built from this project also contain the web page
- no separate frontend build step is required

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MIHOMO_URL` | `http://127.0.0.1:9090` | Mihomo controller URL |
| `MIHOMO_SECRET` | empty | Mihomo bearer token |
| `TRAFFIC_MONITOR_LISTEN` | `:8080` | HTTP listen address |
| `TRAFFIC_MONITOR_DB` | `./traffic_monitor.db` | SQLite database path |
| `TRAFFIC_MONITOR_POLL_INTERVAL_MS` | `2000` | Poll interval in milliseconds |
| `TRAFFIC_MONITOR_RETENTION_DAYS` | `30` | Retention window in days |
| `TRAFFIC_MONITOR_ALLOWED_ORIGIN` | `*` | CORS allow-origin |

Legacy variables `CLASH_API` and `CLASH_SECRET` are also supported.

## Local Run

```bash
go test ./...
go build -o traffic-monitor-enhanced main.go
MIHOMO_URL=http://127.0.0.1:9090 ./traffic-monitor-enhanced
```

Then open:

```text
http://localhost:8080/
```

## Docker

```bash
cp .env.example .env
# edit .env
docker compose up --build -d
```

The default database file is stored at:

```text
./data/traffic_monitor.db
```

## Release Workflow

The repository includes a GitHub Actions workflow for release builds and publishing.

Targets:

- binaries: `linux/amd64`, `linux/arm64`, `windows/amd64`
- Docker images: `linux/amd64`, `linux/arm64`

Docker image name:

```text
zhf883680/clash-traffic-monitor
```

## API

```bash
curl http://localhost:8080/health
curl "http://localhost:8080/api/traffic/aggregate?dimension=sourceIP&start=1713000000000&end=1713086400000"
curl "http://localhost:8080/api/traffic/trend?start=1713000000000&end=1713086400000&bucket=60000"
curl -X DELETE http://localhost:8080/api/traffic/logs
```

## How It Works

- Poll Mihomo `/connections`
- Track per-connection upload and download deltas by connection ID
- Persist deltas into SQLite
- Reset in-memory baselines when Mihomo counters roll back
- Keep historical data across service restarts
