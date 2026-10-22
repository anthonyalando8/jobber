# Prospector: one image for the web/worker process and the migration tool.

# --- build -------------------------------------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/prospector ./cmd/prospector \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/prospector-migrate ./cmd/prospector-migrate

# --- runtime -----------------------------------------------------------------------------------
# Alpine rather than distroless: prospector-migrate's backup-verify shells out to pg_dump and
# pg_restore, which must match the server's major version (postgres:18 in docker-compose.yaml),
# and the health check uses busybox wget. tzdata makes TZ work; daily budgets use local time.
FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata postgresql18-client \
 && addgroup -S -g 10001 prospector \
 && adduser -S -D -H -u 10001 -G prospector prospector
# Templates and static assets are read from disk at request time, relative to the working
# directory, so they keep their repository layout under /app.
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY internal/web/templates ./internal/web/templates
COPY internal/web/static ./internal/web/static
USER prospector
ENV HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/app/bin/prospector"]
