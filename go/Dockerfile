FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go/go.mod go/go.sum* ./
RUN go mod download
COPY go/ ./
RUN CGO_ENABLED=0 go build -o /router ./router

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /router /router
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/router", "-healthcheck"]
ENTRYPOINT ["/router"]
