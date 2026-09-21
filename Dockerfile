FROM lukemathwalker/cargo-chef:0.1.68-rust-1.75 AS chef
WORKDIR /app
FROM chef AS planner
COPY Cargo.toml Cargo.lock ./
COPY crates ./crates
RUN cargo chef prepare --recipe-path recipe.json

FROM chef AS builder
WORKDIR /app
COPY --from=planner /app/recipe.json recipe.json
# Build deps first for better caching
RUN cargo chef cook --release --recipe-path recipe.json
COPY Cargo.toml Cargo.lock ./
COPY crates ./crates
RUN cargo build --release -p thompson-sim && cargo build --release -p control-plane

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=builder /app/target/release/thompson-sim /usr/local/bin/thompson-sim
COPY --from=builder /app/target/release/control-plane /usr/local/bin/control-plane
USER nonroot:nonroot
EXPOSE 8080
# No Docker HEALTHCHECK: distroless has no shell/curl, and probing the binary
# with `--help` always succeeds even when the server is down. Rely on the
# Kubernetes liveness/readiness probes in helm/traverse instead.
HEALTHCHECK NONE
# Default to control-plane (router); override with thompson-sim for harness
ENTRYPOINT ["control-plane"]
CMD ["--help"]
