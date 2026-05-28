# ===========
# Build stage
# ===========
FROM golang:1.25-alpine AS builder

WORKDIR /code

ENV CGO_ENABLED=0

# Pre-install dependencies to cache them as a separate image layer
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . /code
RUN go build -trimpath -ldflags="-s -w" -o sonic-exporter ./cmd/sonic-exporter/main.go

# ===========
# Final stage
# ===========
FROM alpine:3.21

ARG VERSION=unknown
ARG REVISION=unknown
ARG CREATED=unknown
ARG SOURCE=https://github.com/rokernel/sonic-exporter

LABEL org.opencontainers.image.title="sonic-exporter" \
	org.opencontainers.image.description="Prometheus exporter for SONiC switches" \
	org.opencontainers.image.version="${VERSION}" \
	org.opencontainers.image.revision="${REVISION}" \
	org.opencontainers.image.created="${CREATED}" \
	org.opencontainers.image.source="${SOURCE}"

WORKDIR /app
RUN apk --no-cache add ca-certificates curl \
	&& addgroup -S sonic \
	&& adduser -S -G sonic sonic

COPY --from=builder /code/sonic-exporter ./sonic-exporter

EXPOSE 9101

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD curl -fsS http://127.0.0.1:9101/metrics >/dev/null || exit 1

USER sonic

CMD [ "./sonic-exporter" ]
