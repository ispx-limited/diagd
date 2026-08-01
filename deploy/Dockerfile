# Build from the repository root:
#   docker build -f deploy/Dockerfile -t diagd .
# Run with host networking; the TR-471 test connections bind UDP ports
# dynamically, and NAT between subscribers and the server distorts what the
# tests measure:
#   docker run --network host diagd -tr471-bandwidth 5000 -log-format json
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /diagd ./cmd/diagd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /diagd /diagd
ENTRYPOINT ["/diagd", "serve"]
