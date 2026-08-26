# csi-raff — Raff Technologies CSI driver (controller + node in one binary)
FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X github.com/rafftechnologies/csi-raff/driver.DriverVersion=${VERSION}" -o /csi-raff .

# The node plugin formats and mounts volumes on the host: it needs mkfs/fsck
# (e2fsprogs, xfsprogs) and mount/blkid (util-linux) inside the container —
# mount-utils executes them from PATH.
FROM alpine:3.21
RUN apk add --no-cache e2fsprogs e2fsprogs-extra xfsprogs util-linux blkid
COPY --from=build /csi-raff /csi-raff
ENTRYPOINT ["/csi-raff"]
