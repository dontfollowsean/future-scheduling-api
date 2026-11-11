TEST_DATABASE_URL ?= postgres://scheduler:scheduler@localhost:5433/scheduler_test?sslmode=disable
GOLANGCI_LINT_IMAGE := golangci/golangci-lint:v2.15.0

.PHONY: run test lint down

## run: build and start Postgres and the API on :8080
run:
	docker compose up --build

## test: start Postgres, then run every test with the race detector
## (-p 1: each integration package empties the same test database)
test:
	docker compose up -d --wait db
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -race -count=1 -p 1 ./...

## lint: gofmt, go vet, then golangci-lint from its pinned Docker image
lint:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	docker run --rm -v "$(CURDIR)":/app -w /app $(GOLANGCI_LINT_IMAGE) golangci-lint run

## down: stop everything and delete the database volume
down:
	docker compose down -v
