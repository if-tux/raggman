FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /raggman ./cmd/api

FROM debian:bookworm-slim
# poppler-utils: pdftotext for Ingestion. curl: compose healthcheck.
RUN apt-get update \
    && apt-get install -y --no-install-recommends poppler-utils curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /raggman /usr/local/bin/raggman
EXPOSE 8080
ENTRYPOINT ["raggman"]
