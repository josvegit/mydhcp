FROM golang:latest AS builder
WORKDIR /build
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /mydhcp ./cmd/mydhcp && \
    CGO_ENABLED=0 GOOS=linux go build -o /dhcpclient ./cmd/dhcpclient

FROM alpine:3.19
RUN apk add --no-cache ca-certificates wget
COPY --from=builder /mydhcp /usr/local/bin/mydhcp
COPY --from=builder /dhcpclient /usr/local/bin/dhcpclient
ENTRYPOINT ["/usr/local/bin/mydhcp"]
