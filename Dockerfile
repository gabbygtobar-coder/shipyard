# Multi-stage build for the shipyardd API server.
# The final image is distroless and runs as nonroot (uid 65532).

FROM golang:1.22.2-alpine AS build

WORKDIR /src
ENV CGO_ENABLED=0
ENV GOTOOLCHAIN=local

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN go build -trimpath -ldflags="-s -w" -o /out/shipyardd ./cmd/shipyardd

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/shipyardd /shipyardd

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/shipyardd"]
