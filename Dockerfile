# syntax=docker/dockerfile:1
FROM golang:1.23.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/moonchess ./cmd/moonchess

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/moonchess /moonchess
USER nonroot:nonroot
ENTRYPOINT ["/moonchess"]
