# Trainer Video-Call Scheduling API

An HTTP JSON API in Go that lets a client book a 30-minute video call with their trainer, backed by Postgres. A call starts on the hour or half hour, Monday to Friday, between 08:00 and 17:00 Pacific time, and a trainer's calls never overlap. There are three endpoints:

- `GET /trainers/{trainer_id}/availability?starts_at=&ends_at=` lists the free slots in a window.
- `POST /appointments` books one of them.
- `GET /trainers/{trainer_id}/appointments` lists a trainer's appointments.

At startup the server migrates the database and loads the existing appointments from `appointments.json`. The full design, with every alternative considered, is in [docs/PLAN.md](docs/PLAN.md).

## Quick start

```sh
docker compose up --build          # Postgres on :5433, the API on :8080
./scripts/demo.sh                  # in another terminal
```

| Command | Does |
|---|---|
| `make run` | `docker compose up --build` |
| `make test` | Starts Postgres, then runs `go test -race -count=1 -p 1 ./...` (needs Go 1.27) |
| `docker compose --profile test run --rm test` | The same tests inside a `golang:1.27` container, for reviewers without Go |
| `make lint` | `gofmt`, `go vet` and `golangci-lint` (in Docker) |
| `make down` | Stops everything and deletes the database volume |

> **The seed appointments are from January 2019.** Booking in the past is not allowed and past slots are never shown, so availability for those dates is always empty by design (see [Assumptions](#assumptions)). The appointments list still shows them, and the demo books next Monday instead.

[`scripts/demo.sh`](scripts/demo.sh) computes next Monday in Pacific time, so it never needs a hard-coded date. Set `BASE_URL` to point it elsewhere. Its first booking succeeds once per database, so run `make down` before running it a second time. This is real output from a run after `docker compose down -v` and `docker compose up --build` on Wednesday 2026-11-11:

```
# Trainer 1's appointments (the January 2019 seed)
$ curl http://localhost:8080/trainers/1/appointments
{"trainer_id":1,"appointments":[{"id":1,"trainer_id":1,"user_id":1,"starts_at":"2019-01-24T09:00:00-08:00","ends_at":"2019-01-24T09:30:00-08:00"},{"id":2,"trainer_id":1,"user_id":2,"starts_at":"2019-01-24T10:00:00-08:00","ends_at":"2019-01-24T10:30:00-08:00"},{"id":3,"trainer_id":1,"user_id":3,"starts_at":"2019-01-25T10:00:00-08:00","ends_at":"2019-01-25T10:30:00-08:00"},{"id":4,"trainer_id":1,"user_id":4,"starts_at":"2019-01-25T10:30:00-08:00","ends_at":"2019-01-25T11:00:00-08:00"},{"id":5,"trainer_id":1,"user_id":5,"starts_at":"2019-01-26T10:00:00-08:00","ends_at":"2019-01-26T10:30:00-08:00"}]}
HTTP 200

# Trainer 1's availability on Monday 2026-11-16, 08:00 to 11:00
$ curl 'http://localhost:8080/trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T11:00:00-08:00'
{"trainer_id":1,"slots":[{"starts_at":"2026-11-16T08:00:00-08:00","ends_at":"2026-11-16T08:30:00-08:00"},{"starts_at":"2026-11-16T08:30:00-08:00","ends_at":"2026-11-16T09:00:00-08:00"},{"starts_at":"2026-11-16T09:00:00-08:00","ends_at":"2026-11-16T09:30:00-08:00"},{"starts_at":"2026-11-16T09:30:00-08:00","ends_at":"2026-11-16T10:00:00-08:00"},{"starts_at":"2026-11-16T10:00:00-08:00","ends_at":"2026-11-16T10:30:00-08:00"},{"starts_at":"2026-11-16T10:30:00-08:00","ends_at":"2026-11-16T11:00:00-08:00"}]}
HTTP 200

# Book Monday 10:00 (201)
$ curl -X POST http://localhost:8080/appointments -H 'Content-Type: application/json' -d '{"trainer_id": 1, "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}'
{"id":11,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T10:00:00-08:00","ends_at":"2026-11-16T10:30:00-08:00"}
HTTP 201

# Book the same slot again (409)
$ curl -X POST http://localhost:8080/appointments -H 'Content-Type: application/json' -d '{"trainer_id": 1, "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}'
{"error":{"code":"slot_unavailable","message":"trainer 1 already has an appointment overlapping 2026-11-16T10:00:00-08:00 to 2026-11-16T10:30:00-08:00"}}
HTTP 409

# Book Saturday 2026-11-21 10:00 (422)
$ curl -X POST http://localhost:8080/appointments -H 'Content-Type: application/json' -d '{"trainer_id": 1, "user_id": 42, "starts_at": "2026-11-21T10:00:00-08:00", "ends_at": "2026-11-21T10:30:00-08:00"}'
{"error":{"code":"not_business_day","message":"starts_at is on a Saturday in America/Los_Angeles, which is not a business day","field":"starts_at"}}
HTTP 422

# Book Monday 10:15, off the half-hour grid (422)
$ curl -X POST http://localhost:8080/appointments -H 'Content-Type: application/json' -d '{"trainer_id": 1, "user_id": 42, "starts_at": "2026-11-16T10:15:00-08:00", "ends_at": "2026-11-16T10:45:00-08:00"}'
{"error":{"code":"off_grid","message":"starts_at must be on a 30-minute boundary in America/Los_Angeles, with zero seconds","field":"starts_at"}}
HTTP 422
```

**Encoding a `+` offset.** In a query string, a `+` decodes to a space, so `starts_at=2026-11-16T16:00:00+00:00` fails with `400 invalid_field`. Send it as `%2B` (`...T16:00:00%2B00:00`), or use `Z`. Pacific offsets are negative, so the demo never needs this.

## API reference

Requests and responses are JSON. Timestamps are RFC 3339 with an explicit offset (`Z` or `±hh:mm`); any offset is accepted, and every response renders times in Pacific time (`-08:00` or `-07:00`). Ids are positive integers.

| Method | Path | Input | Success |
|---|---|---|---|
| `GET` | `/trainers/{trainer_id}/availability` | Query: `starts_at`, `ends_at` | `200 {"trainer_id", "slots": [{"starts_at", "ends_at"}]}` |
| `POST` | `/appointments` | Body: `trainer_id`, `user_id`, `starts_at`, `ends_at` | `201` with the created appointment |
| `GET` | `/trainers/{trainer_id}/appointments` | none | `200 {"trainer_id", "appointments": [{"id", "trainer_id", "user_id", "starts_at", "ends_at"}]}` |
| `GET` | `/healthz` | none | `200 {"status":"ok"}`, or `503 {"status":"unavailable"}` when the database is down |

**Availability** returns the free 30-minute slots that fit entirely inside the half-open window `[starts_at, ends_at)`, in start order. Slots in the past are left out, so every listed slot is bookable when the response is sent. An empty result is `"slots": []`. The window may be at most 31 days.

**Appointments** are listed by `starts_at`, then `id`, including past ones and the seed's Saturday records. The list is not paginated.

**Errors** all share one shape. `field` appears when one input is at fault, and `code` is stable while `message` may change:

```json
{"error": {"code": "off_grid", "message": "starts_at must be on a 30-minute boundary in America/Los_Angeles, with zero seconds", "field": "starts_at"}}
```

| Status | `code` | When |
|---|---|---|
| 400 | `invalid_json` | The body is not one JSON object of at most 1 MiB, has the wrong types, or has an unknown field, such as the seed file's `started_at` |
| 400 | `invalid_field` | A missing or malformed id or timestamp, including a timestamp without an offset |
| 422 | `invalid_duration` | `ends_at` is not `starts_at` + 30 minutes |
| 422 | `off_grid` | `starts_at` is not on :00 or :30 Pacific, with zero seconds |
| 422 | `not_business_day` | The Pacific date is a Saturday or Sunday |
| 422 | `outside_business_hours` | The call starts before 08:00 or ends after 17:00 Pacific |
| 422 | `in_past` | `starts_at` is before now |
| 422 | `invalid_range` | Availability: `starts_at` is not before `ends_at` |
| 422 | `range_too_large` | Availability: the window is longer than 31 days |
| 409 | `slot_unavailable` | The trainer already has an overlapping appointment. The body does not say whose. |
| 404 | `not_found` | No route matches the path |
| 500 | `internal` | Anything unexpected. The cause is logged with the request id, never sent to the client. |

`POST` checks run in the order of the table and the first failure is returned. A wrong method on a known path, such as `DELETE /appointments`, gets the standard library's plain-text `405` with an `Allow` header rather than JSON. Every response carries an `X-Request-ID` header, which reuses the caller's if one was sent.

## Design decisions and tradeoffs

**The database prevents overlaps.** The table has an exclusion constraint on `(trainer_id, tstzrange(starts_at, ends_at, '[)'))` using `btree_gist`, so the `INSERT` is the availability check and a violation (`23P01`) becomes `409`. A unique index on `(trainer_id, starts_at)` would only stop two calls starting at the same instant. It would let a booking at 13:30 slip inside the seed's 60-minute appointment (id 9) and would need replacing as soon as call lengths vary. The half-open range lets back-to-back calls touch.

**A per-trainer advisory lock queues concurrent bookings.** The plan expected the constraint alone to be enough. In testing, two racing inserts could each wait on the other's uncommitted row, and Postgres then aborted one with `40P01 deadlock_detected` after a one-second stall, which would have been a `500` instead of a `409`. Each insert now runs `pg_advisory_xact_lock(trainer_id)` and then the `INSERT` in one short transaction. The constraint is still the guarantee; the lock only removes the deadlock. The seed takes the same locks, in trainer order.

**Times are stored as UTC and judged in Pacific time.** Columns are `timestamptz`. Every rule converts the instant to `America/Los_Angeles` first and checks the weekday, the hours and the grid on the wall clock, so `2026-11-16T22:30:00+05:45` (08:45 PST) is correctly off the grid. Days are stepped with `time.Date` and `AddDate`, never by adding 24 hours, so slots stay at 08:00 local across DST changes. The tz database is embedded with `time/tzdata`. Responses use the Pacific offset, which matches the seed file and keeps the business hours visible.

**The seed loads every record, even the ones that break the rules.** Five of the ten records are on a Saturday and one lasts 60 minutes. They are existing commitments from another system, so they are loaded as-is and each is logged as a warning; the rules govern new bookings. The load is one transaction with `ON CONFLICT (id) DO NOTHING`, so restarting inserts nothing, and the first booking afterwards gets id 11. A structurally broken file or an overlap fails the whole load. A changed record in a later version of the file is not updated, which is fine for a one-time import. The record-by-record audit is in [PLAN.md section 1](docs/PLAN.md#1-the-seed-data).

**`400`, `422` and `409` mean different things.** `400` says the client sent something unreadable, so it has a bug. `422` says the request was understood and a scheduling rule refused it, so the client can show the message to a person. `409` is a conflict with current state: the same request could have succeeded a moment earlier.

**Three layers.** `httpapi` handles HTTP only: parsing, status codes and JSON. `scheduling` holds every rule and the injected clock, with no I/O, and defines the `Store` interface it needs. `postgres` holds the SQL, the migrations, and the mapping from `23P01` to `scheduling.ErrSlotTaken`. `cmd/scheduler` wires them together. Dependencies point inward: `httpapi → scheduling ← postgres`.

**No framework and no ORM.** Go's `net/http` routes `GET /trainers/{trainer_id}/availability` directly, and the queries are short enough to read as SQL. The only dependencies are `pgx/v5` (the driver, which exposes SQLSTATE and constraint names) and `goose/v3` (embedded SQL migrations). Tests use only the standard library.

## Assumptions

The spec left these open. Each has a default, and the ones that would change the design are listed as questions in [PLAN.md section 11](docs/PLAN.md#11-questions-for-the-hiring-team).

| # | Question | Default chosen |
|---|---|---|
| A1 | Can a client book or see times in the past? | **No (decided).** Past starts get `422 in_past` and past slots are hidden, so the 2019 seed dates show no availability. |
| A2 | What happens to seed records that break the rules? | Loaded as-is with a warning. They block time but are never offered as slots. |
| A3 | The seed file says `started_at`/`ended_at`; the spec says `starts_at`/`ends_at` | The API uses the spec's names; the seed loader maps the file's. |
| A4 | Does "between two dates" mean timestamps or calendar dates? | RFC 3339 timestamps forming a half-open window `[starts_at, ends_at)`. |
| A5 | What about slots that straddle the window's edges? | Only slots that fit entirely inside are returned. |
| A6 | Unknown trainer or user ids | Ids are opaque positive integers; there are no trainers or users tables. An unknown trainer gets the full grid. |
| A7 | May a client hold overlapping calls with two trainers? | Yes; the spec only constrains trainers. Blocking it is one more constraint. |
| A8 | `400` or `422`? | `400` for malformed or missing input, `422` for well-formed input that breaks a rule. |
| A9 | Which time zone do responses use? | Pacific, matching the seed file. |
| A10 | Largest availability window | 31 days (at most 414 slots). |
| A11 | Holidays | Not excluded; the spec says Monday to Friday. |
| A12 | Is `ends_at` redundant in `POST`? | It is required and must equal `starts_at` + 30 minutes. |

Also assumed: one error per response, no authentication, Postgres 17, and availability is a snapshot, so a listed slot can be taken before the client books it (the client then gets `409` and refreshes).

## Testing

```sh
make test                                       # needs Go 1.27 and Docker
docker compose --profile test run --rm test     # needs only Docker
```

Both start Postgres and run every test with the race detector against a separate `scheduler_test` database, so tests never touch the data the demo uses. Without `TEST_DATABASE_URL`, `go test ./...` runs only the unit tests and skips the rest. CI runs `make lint` and the same tests on every pull request.

| Package | What the tests cover |
|---|---|
| `scheduling` | Every booking rule, with a fixed clock: the hour, grid, weekday and duration edges, offsets such as `+05:45`, the past, and the DST weeks of March and November 2026 with their exact UTC instants. Every generated slot passes the booking validator. |
| `httpapi` | One handler case per row of the error table, with a fake store; the error shape; the JSON 404 and the 405. |
| `postgres` | Overlap, back-to-back and different-offset bookings against real Postgres, the `CHECK` constraints, list ordering, and 2-way and 20-way concurrent inserts. |
| `seed` | The real `appointments.json` loads 10 rows with 5 warnings, a rerun inserts nothing, the next id is 11, and a broken or overlapping file writes nothing. |
| End to end | The real handler and store over the seeded database, with the clock pinned to 2019-01-21: trainer 1 has 32 free slots, trainer 2 has 35 and trainer 3 has 36. |

**The concurrency test** (`TestConcurrentBookingOverHTTP`) sends 2, then 20, simultaneous `POST`s for the same slot through the real HTTP server and database, released together by a closed channel. It repeats over 25 slots so the requests genuinely interleave. Each slot must get exactly one `201`, a `409` for every other request, and exactly one row in the database. The store has the same 2-way and 20-way test without HTTP (`TestCreateAppointmentConcurrent`), which is the one that exposed the deadlock described under the advisory lock.

## What I'd do next

| Extension | Where it plugs in |
|---|---|
| Cancel | A `canceled_at` column, the exclusion constraint made partial with `WHERE (canceled_at IS NULL)`, and `POST /appointments/{id}/cancel`. |
| Reschedule | `PATCH /appointments/{id}`, reusing the same rule checks; the constraint already ignores the row's old version. |
| Per-trainer hours or time zones | `scheduling.Rules` is already a value; the service would look it up per trainer from a `trainer_schedules` table. |
| Variable call lengths | `Rules.SlotLength` becomes a set of allowed lengths. The constraint and the slot subtraction are already interval-based. |
| A client's own appointments | `GET /users/{user_id}/appointments`, an index on `(user_id, starts_at)`, and optionally a second exclusion constraint on `user_id` (A7). |
| Pagination | A keyset cursor on `(starts_at, id)`. List responses are already wrapped in an object, so `next_cursor` is an addition. |
| Idempotency keys | An `Idempotency-Key` header on `POST`, so a retried booking returns the original `201` instead of `409`. |
| Liveness and readiness | A liveness endpoint that does not touch the database, beside the current `/healthz`. |
| OpenAPI | A spec for the three endpoints, checked in CI against the handlers. |
| Metrics and traces | Request counts and latencies by route, and traces through to pgx. Logs are already JSON with a request id. |

## How AI tools were used

- **Tool.** The code was written with Claude Code. Each milestone PR (M1 to M6) was written by a Claude Code agent and passed through an automated pipeline (no-mistakes) that runs an AI code review, the tests, a documentation pass and lint before pushing. Commits prefixed `no-mistakes(review)` or `no-mistakes(document)` are that pipeline's fixes.
- **Planning.** [docs/PLAN.md](docs/PLAN.md) was approved before any code was written, and each PR updated it where the code had to differ. Its seed-data audit, constraint results and DST instants came from throwaway scripts run against the real file, an embedded Postgres 17.5 and Go's tz database (Appendix A).
- **Where AI output was corrected:**
  - The plan said the exclusion constraint alone would handle concurrent inserts. The first full test run failed in the 20-way concurrency test with `40P01` deadlocks, so M3 added the per-trainer advisory lock and the plan was updated to match.
  - In M1, the automated review found that a catch-all `/` route turned `POST /healthz` into a `404` without an `Allow` header, contradicting the plan. The routing was changed to keep the standard library's `405`, with tests for both cases.
  - In M3, the review caught that the plan's scratch-run results contradicted the new deadlock finding, and that the test helper emptying a shared database would break once a second package used it. M4 then ran the integration packages one at a time (`-p 1`).
