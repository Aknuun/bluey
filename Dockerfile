FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bluey ./cmd/bluey && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/blueybot ./cmd/blueybot

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

ENV BLUBANK_CHROME_PATH=/usr/bin/chromium
COPY --from=build /out/bluey /usr/local/bin/bluey
COPY --from=build /out/blueybot /usr/local/bin/blueybot

WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bluey"]
