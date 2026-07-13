# Keychain service image.
#
# Build:
#   docker build --target server -t keychain .
#
# Run:
#   docker run -p 8080:8080 -p 9090:9090 \
#     -e KEYCHAIN_POSTGRES_URL=postgres://keychain:keychain@db:5432/keychain?sslmode=disable \
#     keychain

FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine3.23 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/            ./cmd/
COPY gen/            ./gen/
COPY internal/       ./internal/
COPY keychainserver/ ./keychainserver/
COPY pkg/            ./pkg/
COPY proto/          ./proto/
COPY VERSION         ./

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT" \
      -o /bin/keychain \
      ./cmd/keychain

FROM scratch AS server

COPY --from=builder /bin/keychain /bin/keychain
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY VERSION /app/VERSION

EXPOSE 8080 9090

USER 65532:65532
ENTRYPOINT ["/bin/keychain"]
CMD ["serve"]
