FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/ethicguard/ethicguard-api/internal/version.Version=${VERSION} -X github.com/ethicguard/ethicguard-api/internal/version.Commit=${COMMIT}" \
    -o /out/ethicguard-api ./cmd/ethicguard-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ethicguard-api /usr/local/bin/ethicguard-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ethicguard-api"]
