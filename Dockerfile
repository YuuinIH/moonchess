# syntax=docker/dockerfile:1
FROM golang:1.23.3-alpine AS build
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/moonchess ./cmd/moonchess

FROM scratch
COPY --from=build /out/moonchess /moonchess
USER 65532:65532
ENTRYPOINT ["/moonchess"]
