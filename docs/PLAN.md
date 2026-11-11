# Trainer Video-Call Scheduling API: Implementation Plan

This plan covers an HTTP JSON API in Go that lets a client book a 30-minute video call with their trainer. It is written to be reviewed before any code exists. Each milestone in section 12 is one small pull request.

## Summary

**Key recommendations**

- **Time:** Store instants as `timestamptz`, which Postgres keeps as UTC. Accept RFC 3339 timestamps that carry an explicit offset. Evaluate every business rule in `America/Los_Angeles` wall-clock time. Return RFC 3339 rendered in the Pacific offset (`-08:00` or `-07:00`). Build day boundaries with `time.Date` and `AddDate`, never by adding 24 hours to an instant.
- **No overlaps, enforced by the database:** Use a Postgres exclusion constraint on `(trainer_id, tstzrange(starts_at, ends_at, '[)'))` with `btree_gist`. The `INSERT` is the availability check. SQLSTATE `23P01` maps to `409`. In a scratch Postgres 17.5 run, 2 concurrent inserts of one slot gave 1 success and 20 gave 1 success, each time leaving exactly one row. This beats a unique index on `(trainer_id, starts_at)`: the seed file already contains a 60-minute appointment, which a unique index cannot protect.
- **Seed data:** Load all 10 records as-is, in one idempotent transaction. Log a warning for each of the 5 records that break the booking rules, because they are existing commitments. Fail the seed only on structural corruption or an overlap.
- **Stack:** stdlib `net/http` with Go 1.22+ method and wildcard routing, `pgx/v5`, and `goose/v3` running embedded SQL migrations. There are no test dependencies beyond the standard library. Three layers: `httpapi` (transport), `scheduling` (pure rules, no I/O) and `postgres` (storage).
- **Reviewer experience:** `docker compose up` starts Postgres, runs migrations, seeds the data and serves on `:8080`. `make test` runs unit tests plus integration and concurrency tests against a real Postgres.
- **Delivery:** Six milestones, about 5.5 focused hours in total.

**Flagged ambiguities** (each one is discussed in its section, and the ones that would change the design are also listed in section 11)

| # | Ambiguity | Recommendation | Section |
|---|---|---|---|
| A1 | Booking or listing times in the past (the spec is silent, and all seed data is from January 2019) | **Decided:** reject past starts with `422` and hide past slots. Side effect: availability for the 2019 seed dates is always empty. | 3, 5, 11 |
| A2 | Seed records that break the booking rules | Load them as-is and warn. They block time but are never offered as slots. | 1, 11 |
| A3 | The seed file uses `started_at`/`ended_at`, while the spec says `starts_at`/`ends_at` | The API uses the spec's names. The seed loader maps the file's names. | 1, 6 |
| A4 | "Between two dates" could mean timestamps or calendar dates | RFC 3339 timestamps forming a half-open window `[starts_at, ends_at)` | 5, 11 |
| A5 | Slots that straddle the window edges | Return only slots that fit entirely inside the window | 5 |
| A6 | Unknown trainer or user ids (no source of truth exists) | Treat ids as opaque positive integers. No trainers or users tables. | 6, 11 |
| A7 | One client holding overlapping calls with two trainers | Allowed, because the spec constrains only trainers. Blocking it later is one more constraint. | 4, 11 |
| A8 | `400` versus `422` | `400` for malformed or missing input, `422` for well-formed input that breaks a scheduling rule | 3 |
| A9 | Time zone of response timestamps | The Pacific offset, which matches the seed file | 2 |
| A10 | Largest allowed availability range | 31 days, which is about 414 slots at most | 5 |
| A11 | Holidays | Not excluded. The spec says "Monday-Friday". | 11 |
| A12 | Whether `ends_at` is redundant in `POST` | Require it and check that it equals `starts_at + 30m` | 3 |

---

## 1. The seed data

### What the file contains

`appointments.json` has 72 lines: a top-level JSON array of 10 objects. Its fields are:

| Field | JSON type | Notes |
|---|---|---|
| `id` | number (integer value) | 1–10, unique |
| `trainer_id` | number (integer value) | 3 distinct values: 1, 2, 3 |
| `user_id` | number (integer value) | 10 distinct values, with `user_id == id` in every record |
| `started_at` | string | RFC 3339, second precision |
| `ended_at` | string | RFC 3339, second precision |

- **Timestamp format:** `2019-01-24T09:00:00-08:00`. All 20 timestamps use the offset `-08:00`, with no fractional seconds and no `Z`.
- **DST check on the offset:** `-08:00` is correct. US Pacific time in 2019 switched to PDT on Sunday 2019-03-10, so every January 2019 instant is in PST (UTC−8). Each file's wall-clock time is therefore also its Pacific wall-clock time.
- **Time span:** Thursday 2019-01-24 09:00 PST to Saturday 2019-01-26 14:30 PST. Every record is in the past.
- **Counts:** 10 records. 3 trainers: trainer 1 has 5 records, trainer 2 has 2 and trainer 3 has 3. 10 users, one record each.
- **Field naming (A3):** The file says `started_at`/`ended_at`, but the spec says `starts_at`/`ends_at`. The API follows the spec, and the seed loader maps the two names explicitly.

### Every record, checked in Pacific time

Weekdays and wall-clock times below come from converting each instant to `America/Los_Angeles`, not from reading the string.

| id | lines | trainer | user | Pacific start → end | Duration | Verdict |
|---|---|---|---|---|---|---|
| 1 | 2–8 | 1 | 1 | Thu 2019-01-24 09:00 → 09:30 PST | 30m | ok |
| 2 | 9–15 | 1 | 2 | Thu 2019-01-24 10:00 → 10:30 PST | 30m | ok |
| 3 | 16–22 | 1 | 3 | Fri 2019-01-25 10:00 → 10:30 PST | 30m | ok (touches id 4 back-to-back) |
| 4 | 23–29 | 1 | 4 | Fri 2019-01-25 10:30 → 11:00 PST | 30m | ok |
| 5 | 30–36 | 1 | 5 | **Sat** 2019-01-26 10:00 → 10:30 PST | 30m | **breaks rule: weekend** |
| 6 | 37–43 | 2 | 6 | Thu 2019-01-24 09:00 → 09:30 PST | 30m | ok (same instant as id 1 but a different trainer, so no conflict) |
| 7 | 44–50 | 2 | 7 | **Sat** 2019-01-26 10:00 → 10:30 PST | 30m | **breaks rule: weekend** |
| 8 | 51–57 | 3 | 8 | **Sat** 2019-01-26 12:00 → 12:30 PST | 30m | **breaks rule: weekend** |
| 9 | 58–64 | 3 | 9 | **Sat** 2019-01-26 13:00 → 14:00 PST | **60m** | **breaks rules: weekend and wrong duration** |
| 10 | 65–71 | 3 | 10 | **Sat** 2019-01-26 14:00 → 14:30 PST | 30m | **breaks rule: weekend** (touches id 9 back-to-back) |

### Results for each rule

| Rule | Violations |
|---|---|
| Overlap for the same trainer (half-open intervals) | **0** |
| Start or end off the :00/:30 grid | **0** |
| Outside 08:00–17:00 Pacific wall clock | **0**. All times fall between 09:00 and 14:30. The Saturday records are inside the hours but not on a business day. |
| Weekend | **5**: ids 5, 7, 8, 9, 10, all on Saturday 2019-01-26 |
| Duration other than 30 minutes | **1**: id 9 (60 minutes) |
| Duplicates (repeated id, same trainer and start, or identical payload) | **0** |

**Totals:** 5 records break at least one rule, with 6 violations between them. The remaining 5 records (ids 1, 2, 3, 4, 6) are valid apart from being in the past.

### What the data tells the design

- **Back-to-back appointments must be allowed.** Ids 3 and 4, and ids 9 and 10, touch end-to-start, so intervals must be half-open (`[)`).
- **Overlap is per trainer, not global.** Ids 1 and 6 start at the same instant with different trainers, as do ids 5 and 7.
- **Some appointments already last longer than 30 minutes.** Id 9 runs 60 minutes. A guard that only compares start times would let a booking at 13:00–13:30 for trainer 3 through. If id 9 were on a weekday, that booking would overlap it. Section 4 shows this happening.

### How seeding handles rule-breaking records

| Option | Effect on this file | Verdict |
|---|---|---|
| Load as-is | All 10 rows load. Id 9 blocks 13:00–14:00. | **Recommended** |
| Skip and log | Ids 5, 7, 8, 9 and 10 vanish, so a trainer's real commitments become invisible and the times look free. | Hides real data |
| Fail | Startup fails on this exact file and the reviewer cannot run the app. | Unusable |

**Recommendation (A2):** Load the file as-is.

- The seed is a record of appointments that already exist. The booking rules govern *new* bookings made through the API. They do not govern history imported from another system.
- Each record that breaks a rule is logged once at WARN level, with its id and every reason. For example: `seed record breaks booking rules id=9 reasons="duration 1h0m0s, not a business day (Saturday)"`.
- Weekend records never affect availability, because weekend slots are never generated. They still show up in `GET /trainers/{id}/appointments`, as they should.
- This works because of how the rules are split. The database enforces the timeless invariant: no overlap, and `ends_at > starts_at`. Business hours, grid and duration are policy that changes (per-trainer hours, variable lengths are both likely), so that policy lives in Go and is not written into `CHECK` constraints.

**What makes the seed fail** (one transaction, so nothing is partially written):

- Invalid JSON, or unknown fields.
- Unparseable timestamps, or timestamps without an offset.
- `ended_at <= started_at`, or ids that are not positive.
- Duplicate ids within the file.
- An overlap with an existing row, which raises `23P01`.

None of these occur in the current file. They are corruption, and silently "fixing" corruption is worse than stopping.

**Idempotency:** The seed runs inside a single transaction.

```sql
INSERT INTO appointments (id, trainer_id, user_id, starts_at, ends_at)
VALUES ($1, $2, $3, $4, $5)          -- once per record (or one multi-row insert)
ON CONFLICT (id) DO NOTHING;

-- keep the identity sequence ahead of the explicit seed ids
SELECT setval(pg_get_serial_sequence('appointments', 'id'), (SELECT max(id) FROM appointments));
```

These behaviours were checked on a scratch Postgres 17.5 (Appendix A):

- A second run inserts 0 rows.
- The first `POST` after seeding gets id 11.
- `ON CONFLICT (id)` only swallows id conflicts. An overlapping record still raises `23P01`.

**Caveat:** If a later version of the file changes a record that is already loaded, `DO NOTHING` keeps the old row. That is acceptable for a one-time import, and the README says so.

**When seeding runs:** At process start, after migrations and before the server listens, whenever `SEED_FILE` is set. Docker Compose sets it, and production would not.

---

## 2. Time handling

| Concern | Decision |
|---|---|
| Business time zone | `America/Los_Angeles`, loaded once at startup with `time.LoadLocation`. The process fails fast if it cannot be loaded. |
| tz database | Import `time/tzdata` in `main` so DST rules ship inside the binary. This means correctness does not depend on whether the base image or CI runner has `/usr/share/zoneinfo`. The binary grows by about 450 KB. |
| Storage | `timestamptz`, an absolute instant that Postgres keeps as UTC. Never `timestamp without time zone`. The Postgres session `TimeZone` setting is irrelevant. |
| Input | RFC 3339 with an explicit offset (`Z` or `±hh:mm`), parsed with `time.Parse(time.RFC3339, …)`. A missing offset or a space separator gives `400`. Any offset is accepted and normalized to an instant. Fractional seconds parse, then fail the grid rule with `422`. |
| Rule evaluation | Convert to `America/Los_Angeles` first, then check the weekday, the open and close wall-clock times, and the :00/:30 grid on the local time. A non-whole-hour offset shows why this order matters: `2026-11-16T22:30:00+05:45` is 08:45 PST, which is off the grid. |
| Output (A9) | RFC 3339 rendered in `America/Los_Angeles`, such as `2026-11-16T09:00:00-08:00`, produced by one `formatTime` helper. |
| Clock | `now` is injected into the service as `func() time.Time`. Production passes `time.Now` and tests pass a fixed instant. |

**Why return Pacific time rather than UTC (A9):** It matches the seed file. It also makes the business-hours rule visible in the response itself, and RFC 3339 with an offset is still unambiguous for any client. The alternative is `Z` everywhere. That is equally correct, but it hides the 08:00–17:00 rule behind a conversion.

**Driver pitfall:** pgx returns `timestamptz` values in `time.Local`. On the development machine used for Appendix A, rows came back as `-0500 EST`. In a container, `time.Local` is usually UTC. Responses must therefore always go through `formatTime(t, loc)`, which calls `t.In(loc)`. They must never rely on the location of a value scanned from the database.

**Day boundaries are built from wall-clock time:**

- Day `d`'s slots are `time.Date(y, m, d, 8, 0, 0, 0, loc)`, stepped forward in 30-minute increments while the slot's end is no later than `time.Date(y, m, d, 17, 0, 0, 0, loc)`.
- The loop moves from one day to the next with `AddDate(0, 0, 1)`.
- Adding `24*time.Hour` to an instant is wrong across DST. For example, Friday 2026-03-06 08:00 PST + 72h = **Monday 09:00 PDT**, while `AddDate(0, 0, 3)` gives Monday 08:00 PDT, which is correct (Appendix A).
- Opening hours are stored as wall-clock hour and minute values, never as "local midnight + 8h". On a spring-forward day, midnight + 8h is 09:00. That day is a Sunday today, but per-trainer zones or weekend hours would expose the bug.
- Dates are grouped by Pacific calendar date, never by UTC date. Friday's last slot (16:30 PST) is `00:30Z` *Saturday*.

**Bookable window:** A slot needs `08:00 <= start` and `end <= 17:00` Pacific, on Monday to Friday. With 30-minute slots the last start is **16:30**, giving 18 slots per business day. Writing the rule in terms of `end` rather than `start <= 16:30` keeps it correct when appointment lengths start to vary.

**DST never happens inside business hours.** US transitions occur at 02:00 on a Sunday, so a single business day never changes offset. A multi-day window does, which is exactly what the tests below target.

### How a DST-transition week gets tested

These are unit tests with fixed dates and a fixed clock. All expected values were computed in Appendix A.

| Case | Expectation |
|---|---|
| Spring forward. Window Fri 2026-03-06 00:00 PST → Sat 2026-03-14 00:00 PDT. DST starts Sun 2026-03-08. | 108 slots (6 business days × 18). Friday's first slot is `16:00Z` (08:00 PST). Monday 2026-03-09's first slot is `15:00Z` (08:00 PDT) and its last is `23:30Z` (16:30 PDT). No slots on Saturday 7 or Sunday 8 March. Every slot's Pacific wall clock is between 08:00 and 16:30. |
| Fall back. Window Fri 2026-10-30 → Tue 2026-11-03. DST ends Sun 2026-11-01. | Friday's first slot is `15:00Z` (PDT). Monday 2026-11-02's first slot is `16:00Z` (PST). |
| `POST` on Monday 2026-03-09 | `08:00-07:00` is accepted. `08:00-08:00` is 09:00 PDT: accepted, but a different slot. `16:30-08:00` is 17:30 PDT, so `422 outside_business_hours`. |
| `POST` on Friday 2026-03-06 | `16:30-08:00` is accepted, even though its UTC date is the next day. |
| Formatting | A `time.Time` in an unrelated location, such as `Asia/Kathmandu`, still renders with the Pacific offset. |
| Generator and validator agree | Every slot generated in both DST weeks passes the same validator `POST` uses. |

**The fixed clock is required.** These 2026 dates are already in the past, and past-rejection (section 3) would make date-based tests fail unless `now` is pinned (for example to 2026-01-01). The next real transitions are 2027-03-14 and 2027-11-07.

---

## 3. POST validation

Checks run in the order below. The first failure is returned. The validator collects every rule violation, so the seed loader can log them all, but the API reports only the first. Returning all of them later would be a small change.

| # | Check | Status | `code` |
|---|---|---|---|
| 1 | Body is a single JSON object of at most 1 MiB (`http.MaxBytesReader`), with no unknown fields (`DisallowUnknownFields`) and correct types | 400 | `invalid_json` |
| 2 | `trainer_id` and `user_id` are present and are positive integers | 400 | `invalid_field` (plus `field`) |
| 3 | `starts_at` and `ends_at` are present and are RFC 3339 with an offset | 400 | `invalid_field` (plus `field`) |
| 4 | `ends_at - starts_at == 30m` (A12) | 422 | `invalid_duration` |
| 5 | `starts_at` falls on :00 or :30 in Pacific time, with zero seconds and nanoseconds | 422 | `off_grid` |
| 6 | The Pacific date is Monday to Friday | 422 | `not_business_day` |
| 7 | `08:00 <= start` and `end <= 17:00` Pacific | 422 | `outside_business_hours` |
| 8 | `starts_at >= now` (A1, flagged) | 422 | `in_past` |
| 9 | The trainer has no overlapping appointment (enforced by the database, section 4) | 409 | `slot_unavailable` |
| ✓ | Success | **201** | Body is the created appointment |

**Why `400` and `422` are split (A8):** `400` means "I could not understand your request", so the client has a bug. `422` means "I understood it and the scheduling rules say no", so the client can show a message to a person. `409` is reserved for a conflict with *current state*: the same request could have succeeded a moment earlier. `DisallowUnknownFields` turns a field name copied from the seed file, such as `started_at`, into a clear `400` instead of a confusing "missing `starts_at`".

**Past times (A1), not in the spec:**

- *Option A, recommended:* Reject starts before `now` with `422 in_past`. A slot that starts later today is still bookable. Reason: a client cannot have a check-in call in the past, and the rule matches "clients pick from a list of available times" because the list only offers the future (section 5).
- *Option B:* Allow past bookings, which follows the spec literally. This keeps the 2019 seed dates explorable, but lets a client book a call in 2019.
- *Cost of A:* Availability for the seed dates is always empty, and the README says so up front. Integration tests still exercise the seed against availability by pinning the clock to 2019-01-21.
- Section 11, question 1 asks the hiring team. The rule is a single predicate in the service, so changing it touches one function and its tests.

**Error shape**, used for every 4xx and 5xx response:

```json
{
  "error": {
    "code": "outside_business_hours",
    "message": "appointments must start between 08:00 and 16:30 America/Los_Angeles, Monday to Friday",
    "field": "starts_at"
  }
}
```

- `code` is a stable machine-readable string.
- `message` is human-readable and may change between versions.
- `field` is present when one input field is at fault.
- A `500` returns `{"error":{"code":"internal","message":"internal error"}}`. The real error goes to the log together with the request id, which is also returned in the `X-Request-ID` response header.

---

## 4. Overlap prevention that holds under concurrent requests

The rule must hold when two requests race. Go code that runs `SELECT` and then `INSERT` (check-then-act) cannot guarantee that without locks, so the database must enforce it.

| | Unique index on `(trainer_id, starts_at)` | Exclusion constraint on `(trainer_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&)` |
|---|---|---|
| What it guarantees | No two appointments for a trainer start at the same instant | No two appointments for a trainer overlap in time. This is the actual rule. |
| Correct today? | Only because Go also enforces the fixed 30-minute duration and the grid. The guarantee is split between two layers. | Yes, with no reliance on Go |
| Seed record id 9 (60 minutes) | **Fails.** An insert at 13:30 for trainer 3 is accepted (verified) | Rejects 13:30 with `23P01` (verified) |
| Variable-length extension | Must be replaced, along with a data migration and new error mapping | No change needed |
| Back-to-back slots | Fine | Fine because the range is `[)`. A 09:30–10:00 insert next to a 10:00 appointment succeeded in the scratch run. |
| Concurrency | Atomic, error `23505` | Atomic. The second writer waits for the first transaction, then gets `23P01`. |
| Cost | Plain btree, no extension | Needs `btree_gist` (a trusted contrib extension since PG 13 and available on RDS) and a GiST index. Writes are slightly slower, which is irrelevant at this scale. |
| Read support | Availability needs a range query anyway | The same GiST index serves the availability overlap query |

**Recommendation: the exclusion constraint.** It encodes the real invariant. It already protects the seed data's 60-minute appointment. It also makes "variable lengths" a change in Go only.

```sql
-- migrations/00001_create_appointments.sql (goose)
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE appointments (
    id          bigint      GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    trainer_id  bigint      NOT NULL CHECK (trainer_id > 0),
    user_id     bigint      NOT NULL CHECK (user_id > 0),
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT appointments_ends_after_starts CHECK (ends_at > starts_at),
    CONSTRAINT appointments_no_trainer_overlap
        EXCLUDE USING gist (trainer_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&)
);

-- GET /trainers/{id}/appointments: ordered by start, and the key for future keyset pagination
CREATE INDEX appointments_trainer_starts_idx ON appointments (trainer_id, starts_at);
```

- **Identity column:** `GENERATED BY DEFAULT`, not `ALWAYS`, so the seed can insert its own explicit ids.
- **Insert path:** One short transaction: `SELECT pg_advisory_xact_lock(trainer_id)`, then `INSERT … RETURNING id, …`. Nothing is read first. The default `READ COMMITTED` isolation is enough and no `SERIALIZABLE` retry loop is needed.
- **When two transactions race:** Without the lock, Postgres can make each insert wait on the other's uncommitted conflicting row. It then aborts one with `40P01 deadlock_detected` after the 1-second `deadlock_timeout`, instead of `23P01`. M3's store concurrency tests hit this intermittently, in 2-way races as well as 20-way ones. The advisory lock queues a trainer's bookings, so the later insert sees the earlier row once it commits and fails with `23P01`, or succeeds if the earlier transaction rolled back. The constraint is still the guarantee; the lock only removes the deadlock.
- **Error mapping:** The store checks `pgconn.PgError` for `Code == "23P01"` and `ConstraintName == "appointments_no_trainer_overlap"` and returns `scheduling.ErrSlotTaken`. The handler maps that to `409`.
- **Scratch-run results:** These unlocked scratch runs happened not to hit the intermittent `40P01`. 2 concurrent inserts gave 1 success and 1 `23P01`, leaving 1 row. 20 concurrent inserts gave 1 success and 19 `23P01`, leaving 1 row (Appendix A).

Later extensions only add to this constraint:

- **Cancellation:** Becomes `… EXCLUDE … WHERE (canceled_at IS NULL)`.
- **No client double-booking (A7):** A second constraint, `EXCLUDE USING gist (user_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&)`.

---

## 5. Availability algorithm

**Inputs:** `trainer_id` from the path, and `starts_at` and `ends_at` from the query string as RFC 3339.

**Validation:**

| Problem | Status | `code` |
|---|---|---|
| A parameter is missing or unparseable | 400 | `invalid_field` |
| `starts_at >= ends_at` | 422 | `invalid_range` |
| `ends_at - starts_at` is more than 31 days | 422 | `range_too_large` |

**Steps:**

1. **Lower bound.** Set `from = max(starts_at, now)` so past slots are hidden (A1). This is the same predicate `POST` uses, so every listed slot is bookable when the response is sent.
2. **Generate candidates.** For each Pacific calendar day from `from`'s date through `ends_at`'s date, skip Saturday and Sunday. Otherwise emit `[s, s+30m)` for `s = 08:00, 08:30, …, 16:30`, built with `time.Date` (section 2). Keep a candidate only if `s >= from` and `s+30m <= ends_at`.
3. **Load bookings with one query**, which the GiST index can serve:
   ```sql
   SELECT starts_at, ends_at FROM appointments
   WHERE trainer_id = $1 AND tstzrange(starts_at, ends_at, '[)') && tstzrange($2, $3, '[)')
   ORDER BY starts_at;
   ```
4. **Subtract.** Drop each candidate that overlaps any booking (`a.start < b.end && b.start < a.end`). Use an interval-overlap test, not "is this start time in the booked set", so the 60-minute id 9 removes both 13:00 and 13:30, and variable lengths need no change here. A plain nested loop is enough: about 414 candidates at most, against a handful of bookings. A two-pointer merge is the obvious change if the range limit ever grows.
5. **Return** slots in ascending start order. An empty result is `"slots": []` (a non-nil slice), never `null` and never `404`.

| Decision | Recommendation |
|---|---|
| Bounds (A4) | Half-open `[starts_at, ends_at)`. The window from Monday 00:00 to Tuesday 00:00 is exactly Monday. |
| Partial edge slots (A5) | Excluded. A slot is returned only if it fits entirely inside the window. `starts_at=09:10` makes the first slot 09:30. `ends_at=16:45` makes the last slot 16:00–16:30. |
| Maximum range (A10) | 31 days. That spans at most 23 business days × 18 = **414 slots**, plus at most one extra hour if the window crosses spring-forward: a small, bounded response with no pagination needed. |
| Ordering | Ascending by `starts_at` |
| Empty result | `200` with `"slots": []`. This covers a weekend-only window, a fully booked trainer or a window in the past. |
| Past slots (A1) | Hidden, consistent with `POST` |
| Unknown trainer (A6) | The full grid. There is no trainers table, so the API cannot tell a new trainer from an unknown one. |

**Availability is a snapshot.** A listed slot can be taken before the client posts. The client then gets `409 slot_unavailable` and refreshes the list. The database constraint keeps this race safe.

**Seed data in tests:** With the clock pinned to 2019-01-21, the window Thursday 2019-01-24 → Saturday 2019-01-26 (PST) contains 36 candidates per trainer. Expected results:

- **Trainer 1:** 32 slots. Missing are Thursday 09:00 and 10:00 and Friday 10:00 and 10:30.
- **Trainer 2:** 35 slots. Missing is Thursday 09:00.
- **Trainer 3:** 36 slots, because all of its appointments are on Saturday.

---

## 6. API contract

All requests and responses are JSON (`Content-Type: application/json`). Timestamps are RFC 3339 with an offset. The examples use Monday 2026-11-16 (PST), assuming trainer 1 is already booked 09:00–09:30 that day.

| Method | Path | Inputs | Success |
|---|---|---|---|
| GET | `/trainers/{trainer_id}/availability` | Query: `starts_at`, `ends_at` | 200 |
| POST | `/appointments` | Body: `trainer_id`, `user_id`, `starts_at`, `ends_at` | 201 |
| GET | `/trainers/{trainer_id}/appointments` | none | 200 |
| GET | `/healthz` | none | 200 or 503 (section 10) |

**Path style:** Trainer-scoped reads are nested under `/trainers/{trainer_id}`. Go 1.22 path wildcards keep this readable, and the client-appointments extension becomes `/users/{user_id}/appointments`. Booking is `POST /appointments` with `trainer_id` in the body, exactly as the spec lists the fields. A flat alternative is `GET /availability?trainer_id=…` and `GET /appointments?trainer_id=…`. It is equally valid, but its query parameters keep growing as features are added.

**Wrapped lists:** List responses are wrapped in an object. Pagination (`next_cursor`) and other metadata can then be added later without breaking clients.

### GET /trainers/{trainer_id}/availability

```
GET /trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T10:00:00-08:00
```

`200 OK`:

```json
{
  "trainer_id": 1,
  "slots": [
    {"starts_at": "2026-11-16T08:00:00-08:00", "ends_at": "2026-11-16T08:30:00-08:00"},
    {"starts_at": "2026-11-16T08:30:00-08:00", "ends_at": "2026-11-16T09:00:00-08:00"},
    {"starts_at": "2026-11-16T09:30:00-08:00", "ends_at": "2026-11-16T10:00:00-08:00"}
  ]
}
```

`400 Bad Request` when `ends_at` is missing:

```json
{"error": {"code": "invalid_field", "message": "ends_at is required (RFC 3339 with offset)", "field": "ends_at"}}
```

`422 Unprocessable Entity` for a 60-day window:

```json
{"error": {"code": "range_too_large", "message": "the window between starts_at and ends_at must be 31 days or less"}}
```

**URL encoding:** A `+` in a positive offset must be sent as `%2B` in a query string, or it decodes to a space and fails with `400`. Pacific offsets are negative, so the README examples avoid the problem, but it is noted there.

### POST /appointments

```json
{"trainer_id": 1, "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}
```

`201 Created`:

```json
{"id": 11, "trainer_id": 1, "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}
```

`409 Conflict` when the slot is already taken:

```json
{"error": {"code": "slot_unavailable", "message": "trainer 1 already has an appointment overlapping 2026-11-16T09:00:00-08:00 to 2026-11-16T09:30:00-08:00"}}
```

The `409` body does not say who holds the slot.

`422 Unprocessable Entity` when the times are off the grid:

```json
{"error": {"code": "off_grid", "message": "starts_at must be on the hour or half hour", "field": "starts_at"}}
```

`400 Bad Request` when the request uses a field name from the seed file:

```json
{"error": {"code": "invalid_json", "message": "json: unknown field \"started_at\""}}
```

### GET /trainers/{trainer_id}/appointments

```
GET /trainers/1/appointments
```

`200 OK` (seed data shown). Results are ordered by `starts_at`, then `id`. Past appointments and the Saturday seed records are included.

```json
{
  "trainer_id": 1,
  "appointments": [
    {"id": 1, "trainer_id": 1, "user_id": 1, "starts_at": "2019-01-24T09:00:00-08:00", "ends_at": "2019-01-24T09:30:00-08:00"},
    {"id": 2, "trainer_id": 1, "user_id": 2, "starts_at": "2019-01-24T10:00:00-08:00", "ends_at": "2019-01-24T10:30:00-08:00"},
    {"id": 3, "trainer_id": 1, "user_id": 3, "starts_at": "2019-01-25T10:00:00-08:00", "ends_at": "2019-01-25T10:30:00-08:00"},
    {"id": 4, "trainer_id": 1, "user_id": 4, "starts_at": "2019-01-25T10:30:00-08:00", "ends_at": "2019-01-25T11:00:00-08:00"},
    {"id": 5, "trainer_id": 1, "user_id": 5, "starts_at": "2019-01-26T10:00:00-08:00", "ends_at": "2019-01-26T10:30:00-08:00"}
  ]
}
```

- A trainer with no appointments gets `200` with `"appointments": []`.
- `GET /trainers/abc/appointments` gets `400 {"error":{"code":"invalid_field","message":"trainer_id must be a positive integer","field":"trainer_id"}}`.
- The response is unpaginated because the data is small. Pagination is the planned extension (section 7).

**Responses outside the three endpoints:** A catch-all `/` route returns the JSON `404 not_found`. A wrong method on a known path keeps the standard library's `405` with an `Allow` header, which is plain text. That inconsistency is accepted rather than wrapping the router.

---

## 7. Layout and dependencies

```
cmd/scheduler/
  main.go          config from env, slog, pgx pool, migrate, seed, http.Server, signal handling
internal/scheduling/            pure domain: no net/http, no pgx
  rules.go         Rules{Location, Open, Close (wall-clock h:m), SlotLength, Workdays}; Violations, ValidateBooking, ValidateWindow
  slots.go         Interval; Rules.Slots(from, to, now, booked) []Interval
  service.go       Service{store Store, rules Rules, now func() time.Time}: Book, Availability, ListForTrainer
  errors.go        ErrSlotTaken, ValidationError{Code, Field, Message}
internal/postgres/
  store.go         implements scheduling.Store with pgxpool; maps 23P01 to ErrSlotTaken
  migrate.go       goose + embed.FS
  migrations/00001_create_appointments.sql
internal/seed/
  seed.go          decode file, structural checks, warn on Violations, store.SeedAppointments (one tx)
internal/httpapi/
  server.go        routes (Go 1.22 patterns), middleware (request id, logging, recover)
  handlers.go      decode, validate shape, call Service, encode; error to status mapping
  json.go          writeJSON / writeError / formatTime
docker-compose.yml  Dockerfile  Makefile  .golangci.yml  README.md  docs/PLAN.md
```

**Where the layers split:**

- **`httpapi`** owns HTTP concerns only: parsing, the `400`s, status mapping and JSON. It contains no rules and no SQL.
- **`scheduling`** owns every rule and the "now" clock. It defines the `Store` interface it needs, following the Go convention that the consumer defines its interfaces. It can be tested without a network or a database.
- **`postgres`** owns SQL, migrations and the translation of database errors into domain errors.
- **`main`** wires the pieces together and nothing imports it. Dependencies point inward: `httpapi → scheduling ← postgres`.

```go
// scheduling: the only seam between rules and storage
type Store interface {
	CreateAppointment(ctx context.Context, a Appointment) (Appointment, error) // ErrSlotTaken on overlap
	ListByTrainer(ctx context.Context, trainerID int64) ([]Appointment, error)
	ListOverlapping(ctx context.Context, trainerID int64, from, to time.Time) ([]Appointment, error)
}
```

**Dependencies.** Each one is justified below, and nothing else is added.

| Dependency | Why |
|---|---|
| Go standard library `net/http` (Go 1.22+ `METHOD /path/{id}` routing) | Covers routing, path values and `httptest`. A framework would add surface area to defend for no gain. Go 1.23+ also adds `Request.Pattern`, so logs can record the route pattern. Target the current stable Go (1.27; verified locally with go1.27.3). |
| `github.com/jackc/pgx/v5` (`pgxpool`, `pgconn`) | The standard Postgres driver for Go. It provides a connection pool, native `timestamptz`, and typed errors exposing SQLSTATE and constraint names, which the `409` mapping needs. `database/sql` is not needed. |
| `github.com/pressly/goose/v3` | Migrations as plain `.sql` files, embedded with `embed.FS` and run from the binary. There is no separate migration container and the files are readable SQL. It works against a `*sql.DB` through pgx's `stdlib` adapter. *Alternative:* about 40 lines of hand-written runner. That is plausible, but it is code to defend that a well-known tool already covers. |
| Tests: standard library only | `testing`, `httptest` and table tests. No testify or go-cmp; `slices.Equal` and small helpers are enough. |
| Lint: `golangci-lint` v2, pinned Docker image | Run through Docker, so it is not a module dependency and the reviewer needs nothing installed. Default linters plus `gofmt`. |

**Configuration** comes from environment variables, parsed in `main`. The business zone and hours are not configuration: they are domain rules and live in `scheduling.Rules`.

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | none (required) | pgx connection string |
| `HTTP_ADDR` | `:8080` | Listen address |
| `SEED_FILE` | empty (skip seeding) | Path to `appointments.json`. Compose sets it to `/data/appointments.json`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

**Where the likely extensions plug in.** None of these is built now; each needs only a small change in a known place.

| Extension | Where it plugs in | Schema impact |
|---|---|---|
| Cancel | `POST /appointments/{id}/cancel` (or `DELETE`), plus `Service.Cancel` and `Store.Cancel` | Add `canceled_at timestamptz`. Make the exclusion constraint partial with `WHERE (canceled_at IS NULL)`. The availability query adds the same filter. |
| Reschedule | `PATCH /appointments/{id}`. It reuses `Rules.Violations` and runs one `UPDATE`. The constraint already ignores the row's own old version and returns `23P01` on conflict. | None |
| Per-trainer hours or time zones | `Rules` is already a value rather than package constants. `Service` would get rules from a `RulesFor(ctx, trainerID)` lookup instead of one field. `formatTime` would be passed the trainer's zone. | `trainer_schedules (trainer_id, time_zone, open, close, workdays)` |
| Variable lengths | `Rules.SlotLength` becomes an allowed set. `Slots` takes the requested length. Subtraction is already interval-based. | None. The range constraint already covers it. |
| A client's own appointments | `GET /users/{user_id}/appointments`, plus `Store.ListByUser` | Index on `(user_id, starts_at)`. Optionally the user exclusion constraint (A7). |
| Pagination | `?limit=&after=` keyset on `(starts_at, id)`. Responses are already wrapped, so `next_cursor` is purely an addition. | Extend the index to `(trainer_id, starts_at, id)` |
| Idempotent retries | An `Idempotency-Key` header on `POST`, so a retried booking returns the original `201` instead of `409` | Unique `idempotency_key` column |

---

## 8. Testing

| Layer | Kind | What it proves |
|---|---|---|
| `scheduling` | Table-driven unit tests, no I/O, fixed clock | **Validation:** 07:30 rejected and 08:00 accepted; 16:30 accepted and 17:00 rejected; 16:45, `:15` and fractional seconds off the grid; Saturday and Sunday; durations of 0, 29, 31 and 60 minutes and negative durations; `Z`, `+05:45` and `-07:00` inputs; past versus `now`; the DST `POST` cases from section 2. **Slots:** one day (18 slots); a window covering Saturday and Sunday; partial edge windows; an empty window; both DST weeks with their exact UTC instants; subtraction of back-to-back bookings and a 60-minute booking; ascending order; non-nil empty result; every generated slot passes `Violations`. |
| `httpapi` | `httptest` with a fake in-memory `Store` | Status-to-code mapping for each row in section 3; error JSON shape; unknown fields; body too large; a bad `trainer_id` path value; missing or malformed query parameters; `201` body; and `formatTime` output stays in the Pacific offset whatever the `time.Local` setting. |
| `postgres` | Integration against real Postgres | Migrations apply cleanly. Overlap gives `ErrSlotTaken`, including id 9's 13:30 case and an equivalent instant written in a different offset. Back-to-back bookings are allowed. `ends_at <= starts_at` hits the `CHECK`. List ordering is correct. |
| `seed` | Integration | The real `appointments.json` loads 10 rows. 5 warnings are logged (ids 5, 7, 8, 9, 10, with two reasons for id 9). A second run inserts 0 rows. The next `POST` gets id 11. An overlapping file fails with nothing written. |
| End-to-end | `httptest.NewServer(real handler, real store)` | The three endpoints against the seeded database with the clock pinned to 2019-01-21. Trainer 1 gets 32 slots, trainer 2 gets 35 and trainer 3 gets 36 (section 5). |
| **Concurrency** | End-to-end, real Postgres | Two goroutines wait on a closed channel as a start barrier, then `POST` the same slot at once. Exactly one `201` and one `409`, and exactly one row in the database. Repeat over 25 distinct slots so the requests genuinely interleave, then run a 20-way variant. Run everything with `-race`. |

**Isolation and gating:**

- Integration tests read `TEST_DATABASE_URL`. If it is unset they call `t.Skip`, so `go test ./...` works anywhere and runs the unit tests.
- They use a separate `scheduler_test` database, created by a Compose init script, so tests never wipe the data a reviewer is exploring.
- Every test begins with `TRUNCATE appointments RESTART IDENTITY`. Integration tests live in packages that run sequentially (`-p 1` for that target), which keeps them deterministic without per-test schemas.
- There is no coverage target. Instead, every rule in section 3 and every decision in section 5 has at least one named table case.

---

## 9. Reviewer experience

**One command:** `docker compose up --build`.

```yaml
services:
  db:
    image: postgres:17-alpine            # match Future's RDS major version
    environment: {POSTGRES_USER: scheduler, POSTGRES_PASSWORD: scheduler, POSTGRES_DB: scheduler}
    ports: ["5433:5432"]                 # 5433 avoids clashing with a local Postgres
    volumes: ["./scripts/initdb:/docker-entrypoint-initdb.d:ro"]   # CREATE DATABASE scheduler_test
    healthcheck: {test: ["CMD-SHELL", "pg_isready -U scheduler"], interval: 2s, retries: 15}
  api:
    build: .
    depends_on: {db: {condition: service_healthy}}
    environment:
      DATABASE_URL: postgres://scheduler:scheduler@db:5432/scheduler?sslmode=disable
      SEED_FILE: /data/appointments.json
    volumes: ["./appointments.json:/data/appointments.json:ro"]
    ports: ["8080:8080"]
```

**Startup sequence in `api`:**

1. Connect, retrying with backoff for up to about 30 seconds.
2. Run goose migrations from `embed.FS`.
3. Seed if `SEED_FILE` is set.
4. Serve.

In production, migrations would run as a one-off ECS task before the service rolls out, so several replicas never race to migrate. The binary keeps migrate and serve as separate steps, which makes that split easy. The seed file is mounted rather than copied, so the image itself carries no fixture data.

**Dockerfile:**

- **Build stage:** `golang:1.27`. Copy and download modules first for layer caching, then run `CGO_ENABLED=0 go build -trimpath ./cmd/scheduler`.
- **Runtime stage:** `gcr.io/distroless/static`, using the `:nonroot` tag pinned by digest. `ENTRYPOINT ["/scheduler"]` in exec form, so the Go process is PID 1 and receives `SIGTERM` directly.
- The tz database is already embedded through `time/tzdata`.

**Makefile:**

| Target | Does |
|---|---|
| `make run` | `docker compose up --build` |
| `make test` | `docker compose up -d --wait db`, then `TEST_DATABASE_URL=…/scheduler_test go test -race -count=1 ./...` |
| `make lint` | `gofmt -l` (fails on output), `go vet ./...`, then golangci-lint through its pinned Docker image |
| `make down` | `docker compose down -v` (resets the data) |

`make test` needs Go on the host. A `test` profile service in Compose (`docker compose --profile test run --rm test`) runs the same command inside `golang:1.27` for reviewers without Go. It is optional and cut if time runs short.

**README outline:**

1. **What this is.** One paragraph on the problem and the three endpoints.
2. **Quick start.** `docker compose up --build`, plus a curl script covering:
   - list trainer 1's appointments (shows the seed)
   - availability for next Monday
   - book a slot (`201`)
   - book the same slot again (`409`)
   - a Saturday booking (`422`)
   - an off-grid booking (`422`)

   Include a callout: *the seed appointments are from January 2019, so availability for those dates is empty by design (see Assumptions).* Add the `%2B` encoding note.
3. **API reference.** The contract from section 6, condensed.
4. **Design decisions and tradeoffs.**
   - The exclusion constraint compared with a unique index.
   - UTC storage with Pacific rules and output.
   - The seed policy and its audit table.
   - `400`, `422` and `409`.
   - The three layers.
   - Why there is no framework and no ORM.
5. **Assumptions.** Every ambiguity from the summary table, with the default chosen.
6. **Testing.** How to run the tests, and what the concurrency test proves.
7. **What I'd do next.** The extension table from section 7, plus idempotency keys, a liveness/readiness split, OpenAPI, and metrics and traces.
8. **How AI tools were used.**

**AI usage note.** Fill this in honestly as the work happens. Do not write it up front. It should cover:

- which tool was used, and for which tasks: seed-data analysis scripts, plan drafting, scaffolding, generating test tables;
- what was verified by hand: the DDL against a real Postgres, the DST instants, the concurrency result;
- at least one concrete place where AI output was corrected or overruled;
- that every line was reviewed and is owned by the author.

---

## 10. Observability basics

- **Logging:** `log/slog` with a JSON handler on stdout, which ECS ships to CloudWatch. The level comes from `LOG_LEVEL`. On startup, log the config with the `DATABASE_URL` password redacted, the migration version, and a seed summary (`inserted=10 skipped=0 warnings=5`).
- **Request logging middleware:** One line per request with:
  - `method`
  - `route`: the pattern from `r.Pattern`, not the raw path, to keep cardinality low
  - `status` and `bytes`, captured by a small `ResponseWriter` wrapper
  - `duration_ms`
  - `request_id`

  Request bodies are not logged.
- **Request id:** Take `X-Request-ID` from the request if present, otherwise generate one. Store it in the context, echo it in the response header, and attach it to every log line, including the `500` error log.
- **Recover middleware:** Catches a panic, logs it with the stack, and returns the JSON `500`.
- **Health:** `GET /healthz` runs `pool.Ping` with a 2-second timeout and returns `200 {"status":"ok"}` or `503 {"status":"unavailable"}`. ECS and the ALB can poll it. A separate liveness endpoint that does not touch the database is listed under "what I'd do next".
- **Graceful shutdown:**
  1. `signal.NotifyContext` catches `SIGINT` and `SIGTERM`.
  2. `srv.Shutdown` runs with a 10-second deadline, well inside the 30-second default ECS `stopTimeout`. It stops accepting new requests and drains requests in flight.
  3. `pool.Close()` runs.
  4. The process exits with 0.
- **Server timeouts:** `ReadHeaderTimeout` 5s, `ReadTimeout` 10s, `WriteTimeout` 10s, `IdleTimeout` 60s. The request context is passed through to pgx, so a disconnected client cancels its query.

---

## 11. Questions for the hiring team

These are limited to questions whose answers would change the design. Each one lists the assumption the build makes if there is no answer.

**Status:** Question 1 is decided as written below. Questions 2 to 5 have no answer yet, so the build proceeds on their written assumptions.

1. **May clients see or book times in the past?** The seed data is all from January 2019. *This changes validation, the availability filter, and whether the seed data can ever affect availability.* **Decided: no.** `POST` rejects past starts with `422` and availability hides them, so the 2019 dates show no availability (A1).
2. **Are the imported appointments authoritative even when they break the rules?** Five are on a Saturday and one lasts 60 minutes. *This changes the seed policy and whether business rules belong in database `CHECK` constraints.* Assumption: yes. They are real history, so they are loaded and logged, and the database enforces only the overlap rule (A2).
3. **Is there a source of truth for trainers and users, so an unknown `trainer_id` should be a `404`?** *This changes the schema (trainers and users tables with foreign keys) and the error contract.* Assumption: no. Ids are opaque positive integers owned by another service (A6).
4. **May a client hold two overlapping calls with different trainers?** *This decides whether to add a second exclusion constraint on `user_id`.* Assumption: yes, because the spec only constrains trainers (A7).
5. **Does "between two dates" mean RFC 3339 instants or calendar dates, and is the end inclusive?** *This changes the query contract.* Assumption: instants, in a half-open window, keeping only slots that fit entirely inside it (A4, A5).

Everything else is a written assumption in the README:

- Output in Pacific time (A9).
- A 31-day limit on availability requests (A10).
- No holiday calendar (A11).
- `ends_at` required and checked (A12).
- One error returned per response.
- No authentication.
- Positive `int64` ids.
- Postgres 17.
- Availability is a snapshot that can race with `409`.
- The appointments list is unpaginated.

---

## 12. Milestones

Each milestone is one reviewable pull request that ships with its own tests and leaves `main` green. The order puts the riskiest logic (time rules) and the core guarantee (the overlap constraint) before the HTTP layer.

| # | PR | Scope | Tests included | Estimate |
|---|---|---|---|---|
| M1 | **Skeleton and one-command run** | `go.mod`; `cmd/scheduler` with env config, slog, pgx pool connect and retry, and `/healthz`; request-id, logging and recover middleware; graceful shutdown; Dockerfile; `docker-compose.yml` with the `scheduler_test` init script; Makefile (`run`, `test`, `lint`, `down`); `.golangci.yml`; this plan as `docs/PLAN.md`; a stub README | Config parsing table test; `/healthz` 200 and 503 with a fake pinger; middleware records status and request id | 45 min |
| M2 | **Scheduling rules and slot generation** (pure Go) | `scheduling.Rules`, `Violations`, `Slots`, `formatTime`, errors and validation codes; `time/tzdata` | The full validation table and slot tables, including both DST weeks, edge windows, 60-minute and back-to-back subtraction, and "every slot passes `Violations`"; all with a fixed clock | 75 min |
| M3 | **Postgres schema and store** | goose migration (the section 4 DDL); `Migrate` at startup; `postgres.Store` (`CreateAppointment`, `ListByTrainer`, `ListOverlapping`); `23P01` mapped to `ErrSlotTaken`; integration-test helper gated on `TEST_DATABASE_URL` | Constraint behaviour (overlap, back-to-back, different offset, `CHECK`); list ordering; a store-level 2-way and 20-way concurrent insert | 60 min |
| M4 | **Idempotent seed** | `internal/seed`: decode with field mapping, structural checks, warnings from `Violations`, one-transaction `ON CONFLICT (id) DO NOTHING` plus `setval`; wired to `SEED_FILE` | Real `appointments.json`: 10 rows and 5 warnings; rerun gives 0 inserts; next id is 11; an overlapping fixture fails with nothing written; a corrupt fixture fails | 35 min |
| M5 | **HTTP API** | `scheduling.Service` (`Book`, `Availability`, `ListForTrainer`, with the clock injected); `httpapi` routes and handlers; JSON error mapping; JSON 404 catch-all | Handler tables with a fake store, one per section 3 row; end-to-end over seeded Postgres with the clock pinned to 2019-01-21 (32, 35 and 36 slots); **the HTTP concurrency test (exactly one 201 and one 409)** | 75 min |
| M6 | **README and reviewer polish** | Full README following the section 9 outline, with curl examples run against `docker compose up`; the AI usage note; optional Compose `test` profile; optional 20-line GitHub Actions workflow (lint plus test with a Postgres service), cut if short on time | Run the README's curl script end to end; `make lint` and `make test` clean from a fresh clone | 40 min |

**Total: about 5.5 hours.**

**Dependencies between PRs:**

- M2 and M3 are independent of each other, and both build on M1.
- M4 needs M2 for `Violations` and M3 for the store.
- M5 needs M2 and M3.
- M6 comes last.

**Before the live session:**

- Tag the final commit.
- Rehearse the extension table from section 7. The likely live asks, cancel and variable lengths, are each a change to one constraint or one function.

---

## Appendix A: How the findings were verified

The figures above come from throwaway scripts, which are not committed. They were run with go1.27.3 on darwin/amd64.

**Seed analysis.** A Go program decoded `appointments.json` twice: once generically, to list the keys and JSON types, and once with `DisallowUnknownFields` and `UseNumber`. It parsed each timestamp as RFC 3339, converted it to `America/Los_Angeles`, and checked duration, grid, weekday and 08:00–17:00. It also checked half-open overlap, adjacency and duplicates within each trainer. Key output:

```
records: 10
  key ended_at    types=map[string:10]
  key id          types=map[float64:10]
  key started_at  types=map[string:10]
  key trainer_id  types=map[float64:10]
  key user_id     types=map[float64:10]
id=5  trainer=1 start=2019-01-26T10:00:00-08:00 (Saturday 10:00, LA zone PST off -8) dur=30m0s issues=[weekend (Saturday)]
id=7  trainer=2 start=2019-01-26T10:00:00-08:00 (Saturday 10:00, LA zone PST off -8) dur=30m0s issues=[weekend (Saturday)]
id=8  trainer=3 start=2019-01-26T12:00:00-08:00 (Saturday 12:00, LA zone PST off -8) dur=30m0s issues=[weekend (Saturday)]
id=9  trainer=3 start=2019-01-26T13:00:00-08:00 (Saturday 13:00, LA zone PST off -8) dur=1h0m0s issues=[duration 1h0m0s (want 30m) weekend (Saturday)]
id=10 trainer=3 start=2019-01-26T14:00:00-08:00 (Saturday 14:00, LA zone PST off -8) dur=30m0s issues=[weekend (Saturday)]
(ids 1, 2, 3, 4, 6: issues=[])
trainers: map[1:5 2:2 3:3]
users: distinct 10      ids: distinct 10
offset suffixes: map[-08:00:20]
span: 2019-01-24 09:00:00 -0800 -> 2019-01-26 14:30:00 -0800
adjacent (touching, not overlapping) trainer 3: id 9 and id 10
adjacent (touching, not overlapping) trainer 1: id 3 and id 4
same start, different trainers: id 6 (t2) and id 1 (t1)
same start, different trainers: id 7 (t2) and id 5 (t1)
```

There were no `OVERLAP` lines.

**Constraint and seeding behaviour.** The section 4 DDL was run on an embedded PostgreSQL 17.5 (`fergusstrange/embedded-postgres`) through `pgx/v5`, with the following results:

```
ddl: ok
seed #1 (adjacent pairs 3/4 and 9/10): ok 4
seed #2 (idempotent): ok 0
first generated id after setval: 11 ok
13:30 slot inside 60-min id 9 (exclusion): 23P01 appointments_no_trainer_overlap
ON CONFLICT (id) DO NOTHING still raises overlap: 23P01 appointments_no_trainer_overlap
adjacent before id 3 (allowed by '[)'): ok
zero-length (check): 23514 appointments_ends_after_starts
same instant, different offset (10:15-10:45 PST as Z) overlaps id 3/4: 23P01 appointments_no_trainer_overlap
unique-index table accepts overlapping 13:30 under 60-min 13:00: ok
concurrent n=2: map[23P01 appointments_no_trainer_overlap:1 ok:1] rows=1 (21ms)
concurrent n=20: map[23P01 appointments_no_trainer_overlap:19 ok:1] rows=1 (81ms)
overlap query row: 9 2019-01-26 16:00:00 -0500 EST Local ...   <- pgx scans timestamptz into time.Local
```

**DST instants**, computed with `time.LoadLocation("America/Los_Angeles")`:

```
2019 offset change effective by Sun 2019-03-10 -8 -> -7;  Sun 2019-11-03 -7 -> -8
2026 offset change effective by Sun 2026-03-08 -8 -> -7;  Sun 2026-11-01 -7 -> -8
2027 offset change effective by Sun 2027-03-14 -8 -> -7;  Sun 2027-11-07 -7 -> -8
Fri 2026-03-06 first=2026-03-06T08:00:00-08:00 (16:00Z) last=2026-03-06T16:30:00-08:00 (00:30Z)
Mon 2026-03-09 first=2026-03-09T08:00:00-07:00 (15:00Z) last=2026-03-09T16:30:00-07:00 (23:30Z)
Fri 2026-10-30 first=2026-10-30T08:00:00-07:00 (15:00Z) last=2026-10-30T16:30:00-07:00 (23:30Z)
Mon 2026-11-02 first=2026-11-02T08:00:00-08:00 (16:00Z) last=2026-11-02T16:30:00-08:00 (00:30Z)
naive +72h from Fri 08:00 PST: 2026-03-09T09:00:00-07:00 vs AddDate: 2026-03-09T08:00:00-07:00
2026-03-09T16:30:00-08:00 in LA: 2026-03-09T17:30:00-07:00
2026-11-16T22:30:00+05:45 in LA: 2026-11-16T08:45:00-08:00
"2026-11-16T09:00:00.5-08:00" parses (nanos=500000000); "2026-11-16T09:00:00" fails (no offset)
slots Fri 03-06..Fri 03-13: 108
```
