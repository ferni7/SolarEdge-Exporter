ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /see-build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o solaredge-exporter .
RUN apk add --no-cache tzdata && adduser -D solaredge && mkdir -p /runtime && cp solaredge-exporter /runtime/ && chown -R solaredge:solaredge /runtime

FROM alpine:3.22
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
ENV INVERTER_ADDRESS=192.168.1.189
ENV INVERTER_PORT=502
ENV EXPORTER_INTERVAL=5
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
# Copy the writable home directory without running target-platform binaries.
COPY --from=build --chown=solaredge:solaredge /runtime /home/solaredge
WORKDIR /home/solaredge
USER solaredge
CMD ["./solaredge-exporter"]
