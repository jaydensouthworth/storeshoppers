FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/shop ./cmd/shop

FROM debian:bookworm-slim
RUN groupadd --gid 10001 shopper && useradd --uid 10001 --gid 10001 --no-create-home shopper \
    && mkdir -p /data && chown 10001:10001 /data && chmod 700 /data
COPY --from=build /out/shop /usr/local/bin/shop
USER 10001:10001
ENV APP_ADDR=0.0.0.0:8090 DATABASE_PATH=/data/shop.db
EXPOSE 8090
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/shop", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/shop"]
