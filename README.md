# Trainer Video-Call Scheduling API

An HTTP JSON API in Go that lets a client book a 30-minute video call with their trainer, backed by Postgres.

This is a work in progress. So far it has the skeleton (configuration, logging, a database connection, `GET /healthz` and the one-command run), the Postgres schema, migrated at startup, the scheduling rules and slot generation, an idempotent seed that loads `appointments.json` at startup when `SEED_FILE` is set (Docker Compose sets it), and the three scheduling endpoints (`GET /trainers/{trainer_id}/availability`, `POST /appointments` and `GET /trainers/{trainer_id}/appointments`, specified in [docs/PLAN.md section 6](docs/PLAN.md#6-api-contract)). Full usage docs are coming in a later milestone.

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
