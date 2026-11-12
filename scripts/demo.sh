#!/usr/bin/env bash
# demo.sh walks through the API against a running server, usually
# `docker compose up --build`. It books next Monday, computed in Pacific
# time, so it never needs a hard-coded date. BASE_URL overrides the server.
#
# The first booking succeeds only once per database; run `make down` and
# start again to reset it.
set -euo pipefail

BASE_URL=${BASE_URL:-http://localhost:8080}
export TZ=America/Los_Angeles

# pacific_date prints the date N days from today, as YYYY-MM-DD.
pacific_date() {
	if date -v+1d >/dev/null 2>&1; then
		date -v+"$1"d +%Y-%m-%d # BSD date (macOS)
	else
		date -d "+$1 days" +%Y-%m-%d # GNU date (Linux)
	fi
}

# pacific_time prints DATE at HH:MM Pacific as RFC 3339, such as
# 2026-11-16T09:00:00-08:00. The offset is -08:00 or -07:00 depending on DST.
pacific_time() {
	local offset
	if date -v+1d >/dev/null 2>&1; then
		offset=$(date -j -f '%Y-%m-%d %H:%M' "$1 $2" +%z)
	else
		offset=$(date -d "$1 $2" +%z)
	fi
	echo "$1T$2:00${offset:0:3}:${offset:3:2}"
}

# show prints a curl command, quoting the arguments that need it, then
# runs it and prints the body and status.
show() {
	printf '$ curl'
	for arg in "$@"; do
		case $arg in
		*[\ \"\&\?]*) printf " '%s'" "$arg" ;;
		*) printf ' %s' "$arg" ;;
		esac
	done
	echo
	curl -sS -w '\nHTTP %{http_code}\n\n' "$@"
}

# %u is 1 for Monday, so 8-%u is 1 to 7 days ahead: never today.
monday=$(pacific_date $((8 - $(date +%u))))
saturday=$(pacific_date $((8 - $(date +%u) + 5)))

echo "# Trainer 1's appointments (the January 2019 seed)"
show "$BASE_URL/trainers/1/appointments"

echo "# Trainer 1's availability on Monday $monday, 08:00 to 11:00"
show "$BASE_URL/trainers/1/availability?starts_at=$(pacific_time "$monday" 08:00)&ends_at=$(pacific_time "$monday" 11:00)"

booking="{\"trainer_id\": 1, \"user_id\": 42, \"starts_at\": \"$(pacific_time "$monday" 10:00)\", \"ends_at\": \"$(pacific_time "$monday" 10:30)\"}"

echo "# Book Monday 10:00 (201)"
show -X POST "$BASE_URL/appointments" -H 'Content-Type: application/json' -d "$booking"

echo "# Book the same slot again (409)"
show -X POST "$BASE_URL/appointments" -H 'Content-Type: application/json' -d "$booking"

echo "# Book Saturday $saturday 10:00 (422)"
show -X POST "$BASE_URL/appointments" -H 'Content-Type: application/json' \
	-d "{\"trainer_id\": 1, \"user_id\": 42, \"starts_at\": \"$(pacific_time "$saturday" 10:00)\", \"ends_at\": \"$(pacific_time "$saturday" 10:30)\"}"

echo "# Book Monday 10:15, off the half-hour grid (422)"
show -X POST "$BASE_URL/appointments" -H 'Content-Type: application/json' \
	-d "{\"trainer_id\": 1, \"user_id\": 42, \"starts_at\": \"$(pacific_time "$monday" 10:15)\", \"ends_at\": \"$(pacific_time "$monday" 10:45)\"}"
