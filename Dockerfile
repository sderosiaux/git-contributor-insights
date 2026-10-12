FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ghca ./cmd/ghca

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 atlas && useradd --uid 10001 --gid atlas --create-home atlas \
    && mkdir -p /data && chown atlas:atlas /data
COPY --from=build /ghca /usr/local/bin/ghca
USER atlas
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8789
ENTRYPOINT ["ghca"]
CMD ["serve", "--listen", "0.0.0.0:8789", "--data-dir", "/data"]
