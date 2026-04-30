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

WORKDIR /app
RUN apk --no-cache add ca-certificates curl \
	&& addgroup -S sonic \
	&& adduser -S -G sonic sonic

COPY --from=builder /code/sonic-exporter ./sonic-exporter

USER sonic

CMD [ "./sonic-exporter" ]
