FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY conf ./conf
RUN CGO_ENABLED=0 go build -tags=nomsgpack -trimpath -ldflags='-s -w' -o /forge ./cmd/server
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /forge /forge
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY conf /conf
USER 65532:65532
ENV GIN_MODE=release
EXPOSE 8080
ENTRYPOINT ["/forge"]
