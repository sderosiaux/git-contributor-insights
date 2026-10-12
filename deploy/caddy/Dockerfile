FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /caddy .

# Rebuild the official proxy with the patched Go runtime and HTTP libraries.
FROM caddy:2.11.7-alpine
COPY --from=build /caddy /usr/bin/caddy
