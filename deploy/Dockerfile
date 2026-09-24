# Single-container LSM: one static binary, SQLite on a volume.
# The same image runs locally (deploy/compose.yml) and on EC2.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY web ./web
# modernc.org/sqlite is pure Go: no cgo, fully static binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lsm ./cmd/lsm \
 && mkdir -p /out/data /out/backup

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lsm /lsm
COPY seed /seed
# Pre-create the mount points owned by nonroot so fresh named volumes
# inherit writable ownership.
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY --from=build --chown=nonroot:nonroot /out/backup /backup

ENV LSM_ADDR=:8080 \
    LSM_DB_PATH=/data/lsm.db \
    LSM_BACKUP_DIR=/backup \
    LSM_SEED_DIR=/seed

# /data: the database. /backup: scheduled exports — mount a separate disk
# here on EC2 so it can be snapshotted independently.
VOLUME ["/data", "/backup"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/lsm"]
CMD ["serve"]
