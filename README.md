# Trainer Video-Call Scheduling API

An HTTP JSON API in Go that lets a client book a 30-minute video call with their trainer, backed by Postgres.

This is a work in progress. So far it has the skeleton (configuration, logging, a database connection, `GET /healthz` and the one-command run) and the Postgres schema, migrated at startup. The scheduling endpoints are coming in later milestones.

## Quick start

```sh
docker compose up --build
curl -i localhost:8080/healthz   # 200 {"status":"ok"}
```

| Command | Does |
|---|---|
| `make run` | `docker compose up --build` |
| `make test` | Starts Postgres, then runs `go test -race ./...` (needs Go 1.27) |
| `make lint` | `gofmt`, `go vet` and `golangci-lint` (in Docker) |
| `make down` | Stops everything and deletes the database volume |

## Design

The design, assumptions and milestones are in [docs/PLAN.md](docs/PLAN.md).
