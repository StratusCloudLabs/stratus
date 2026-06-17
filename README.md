# stratus-runtime

A self-hostable, single-binary runtime for orchestrating ephemeral
GitHub Actions self-hosted runners and hardware-isolated sandboxes on
Kubernetes.

`stratus-runtime` combines four components in one Go binary:

- **JIT runner controller** — watches a queue of pending runner requests,
  atomically claims each one, and creates the corresponding Kubernetes
  `Job` for a Just-In-Time, ephemeral GitHub Actions runner. Concurrency
  safety is achieved with a transactional claim that flips a document from
  `pending` to `scheduling` before the Kubernetes API call, preventing
  duplicate Job creation across controller replicas.
- **Kata sandbox controller** — creates and reaps
  [Kata Containers](https://katacontainers.io/) sandbox pods using the
  `kata` runtime class for hardware-backed VM isolation.
- **HMAC webhook proxy** — validates incoming GitHub webhook signatures
  (`X-Hub-Signature-256` / `X-Hub-Signature`, HMAC-SHA256/SHA1) and proxies
  verified payloads to a configurable upstream ingest service.
- **Job reaper & metrics** — reaps completed runner Jobs and exposes
  Prometheus metrics for queue depth, claim latency, Job creation, sandbox
  operations, and webhook processing.

## Architecture

```
GitHub  ──webhook──▶  HMAC proxy  ──▶  upstream ingest (your service)
                                          │
                                          ▼
                              runner-request queue (Firestore)
                                          │
                          ┌───────────────┴───────────────┐
                          ▼                                ▼
                  JIT controller                      job reaper
                  (claim + schedule)                  (cleanup)
                          │
                          ▼
                  Kubernetes Jobs ──▶ ephemeral GitHub Actions runners
                                       (+ optional DinD sidecar)

      sandbox controller ──▶ Kata Container sandbox pods (kata runtimeClass)
```

The runner-request queue is backed by Google Cloud Firestore in the
reference implementation. The storage layer is isolated behind the
controller package; swapping it for another backend requires implementing
the same claim/update semantics.

## HTTP API

| Method | Path                 | Description                          |
|--------|----------------------|--------------------------------------|
| GET    | `/health`            | Liveness                             |
| GET    | `/ready`             | Readiness                            |
| GET    | `/metrics`           | Prometheus metrics                   |
| POST   | `/jit/runner`        | Submit a JIT runner request          |
| GET    | `/jit/runner/{id}`   | Get runner status                    |
| DELETE | `/jit/runner/{id}`   | Delete / clean up a runner           |
| POST   | `/sandbox/{id}`      | Create a Kata sandbox                |
| GET    | `/sandbox/{id}`      | Get sandbox status                   |
| DELETE | `/sandbox/{id}`      | Delete a sandbox                     |
| POST   | `/webhook/github`    | Ingest a (signed) GitHub webhook     |

A metrics-only server is also exposed on a separate port (`/metrics`,
`/healthz`).

## Build & run

```bash
# Build the binary
go build -o stratus-runtime ./cmd/stratus-runtime/

# Or build the container image
docker build -t stratus-runtime:dev .

# Run (see Configuration below for all variables)
GITHUB_WEBHOOK_SECRET=... \
GOOGLE_CLOUD_PROJECT=your-gcp-project \
RUNNER_NAMESPACE=arc-runners \
GHCR_BASE=ghcr.io/stratuscloudlabs \
./stratus-runtime
```

The binary degrades gracefully: if Firestore or Kubernetes are
unavailable, the dependent controllers are disabled and the HTTP server
still serves health/metrics/webhook endpoints.

## Configuration

All configuration is via environment variables. See
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) and
[`.env.example`](.env.example) for the full list.

## License

Apache License 2.0. Copyright 2026 Scheduler Systems Ltd. See
[`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
