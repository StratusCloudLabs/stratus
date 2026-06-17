# Configuration

`stratus-runtime` is configured entirely through environment variables.
All values have defaults suitable for local development; production
deployments must set the secrets.

## Server

| Variable            | Default | Description                                |
|---------------------|---------|--------------------------------------------|
| `PORT`              | `8080`  | Main HTTP API port.                        |
| `METRICS_PORT`      | `9091`  | Prometheus metrics / `/healthz` port.      |
| `SHUTDOWN_TIMEOUT`  | `30s`   | Graceful shutdown timeout.                 |

## GCP / Firestore

| Variable                         | Default     | Description                                          |
|----------------------------------|-------------|------------------------------------------------------|
| `GOOGLE_CLOUD_PROJECT`           | _(unset)_   | GCP project ID hosting the Firestore queue.          |
| `FIRESTORE_DB`                   | `(default)` | Firestore database ID.                               |
| `GOOGLE_APPLICATION_CREDENTIALS` | _(unset)_   | Path to a service-account key file (optional; uses ADC otherwise). |

## Kubernetes

| Variable           | Default       | Description                                        |
|--------------------|---------------|----------------------------------------------------|
| `KUBECONFIG`       | _(unset)_     | Path to a kubeconfig. Empty = in-cluster config.   |
| `RUNNER_NAMESPACE` | `arc-runners` | Namespace where runner Jobs are created.           |
| `GHCR_BASE`        | `ghcr.io/stratuscloudlabs` | Container registry base for runner images. |
| `GHCR_SECRET`      | `ghcr-secret` | Name of the imagePullSecret for the registry.      |
| `SANDBOX_IMAGE`    | `ghcr.io/stratuscloudlabs/sandbox-kata:latest` | Image used for Kata sandbox pods. |

## Webhook

| Variable                | Default                                   | Description                                              |
|-------------------------|-------------------------------------------|----------------------------------------------------------|
| `GITHUB_WEBHOOK_SECRET` | _(unset, **required**)_                   | HMAC secret for verifying GitHub webhook signatures.     |
| `WEBHOOK_UPSTREAM_URL`  | `http://localhost:8080/webhooks/github`   | Upstream service that verified payloads are proxied to.  |

## JIT controller

| Variable                       | Default | Description                                                |
|--------------------------------|---------|------------------------------------------------------------|
| `STRATUS_INSTALLATION_ID`      | _(unset)_ | Installation scope for capacity checks & metrics push.   |
| `METRICS_PUSH_INTERVAL`        | `30s`   | How often cluster metrics are pushed to the queue store.   |
| `REAPER_INTERVAL`              | `30s`   | Job reaper sweep interval.                                 |
| `MIN_SCHEDULED_AGE`            | `2m`    | Grace period before a scheduled Job is eligible for reaping.|
| `RETRY_BASE_INTERVAL`          | `30s`   | Base backoff for capacity-deferred runner requests.        |
| `RETRY_MAX_INTERVAL`           | `5m`    | Max backoff for capacity-deferred runner requests.         |
| `STARTUP_RECOVERY_THRESHOLD`   | `2m`    | Age above which stuck `scheduling` docs are reset on boot. |

## Auth (optional)

| Variable                     | Default           | Description               |
|------------------------------|-------------------|---------------------------|
| `STRATUS_AUTH_JWT_SECRET`    | _(unset)_         | JWT signing secret.       |
| `STRATUS_AUTH_JWT_ISSUER`    | `stratus`         | Expected JWT issuer.      |
| `STRATUS_AUTH_JWT_AUDIENCE`  | `stratus-runtime` | Expected JWT audience.    |

## Misc

| Variable          | Default | Description            |
|-------------------|---------|------------------------|
| `STRATUS_VERSION` | `dev`   | Reported build version.|
| `DEBUG`           | _(unset)_ | Any value enables debug logging. |
