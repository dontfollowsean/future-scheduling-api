# Build stage: download modules first so they cache separately from source.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /scheduler ./cmd/scheduler

# Runtime stage: static binary on distroless, running as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /scheduler /scheduler
EXPOSE 8080
# Exec form, so the Go process is PID 1 and receives SIGTERM directly.
ENTRYPOINT ["/scheduler"]
