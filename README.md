# Shipyard

Shipyard is a small platform-as-a-service. Give it a GitHub repository and, in later milestones, it builds a container image and runs that image for you. The project is a learning portfolio: a Go control plane, a local runtime, then a single AWS path. It is not a general-purpose cloud.

Milestone M0 is the control plane skeleton. `shipyardd` is an HTTP API and `shipyard` is a CLI. They register applications in memory. M0 does not clone repositories, build images, start containers, or call AWS.

## Roadmap

| Milestone | What it adds |
| --- | --- |
| M0 | HTTP API, CLI, and an in-memory app and deployment store |
| M1 | Build a Docker image from a Git repository |
| M2 | Run that image locally and route traffic with a reverse proxy |
| M3 | Push the image to AWS ECR and run it on ECS Fargate, provisioned with Terraform |
| M4 | Push-to-deploy from GitHub, plus rollback to a previous deployment |
| M5 | Health checks, metrics, operator docs, and a cost note for the AWS path |

## Out of scope

Shipyard will not grow teams, billing, Kubernetes, multi-cloud support, or custom domains. Those are real PaaS problems, and they are someone else's product.

## Quickstart

Install Go 1.22 or newer, then:

```bash
make build
make run
```

`shipyardd` listens on `:8080`. `SHIPYARD_ADDR` or `--addr` changes that.

In another shell:

```bash
./bin/shipyard apps create --name demo --repo https://github.com/golang/example
./bin/shipyard apps list
./bin/shipyard apps get demo
curl -s localhost:8080/healthz
```

The CLI talks to `http://127.0.0.1:8080` unless you pass `--api-url` or set `SHIPYARD_API_URL`. Put `--api-url` before the `apps` command.

`apps create` and `apps get` print one app as indented JSON. `apps list` prints `{"apps":[...]}`. `created_at` is UTC. Names are stored trimmed, and repository URLs are stored in canonical form (`https://github.com/<owner>/<repo>`, without a trailing `.git`).

### API

| Method and path | Behavior |
| --- | --- |
| `GET /healthz` | `{"status":"ok"}` |
| `POST /apps` | Register an app. Body: `{"name":"...","repo_url":"..."}`. `201` with the app, `Location: /apps/<name>`. |
| `GET /apps` | List apps in creation order: `{"apps":[...]}`. |
| `GET /apps/{name}` | Fetch one app. |

`POST /apps` requires `Content-Type: application/json` and rejects unknown fields. Errors from these routes are JSON: `{"error":"..."}`. A path that matches no route returns net/http's plain-text 404. A known path with the wrong method returns 405 and an `Allow` header.

Validation:

- **name** is a slug of 1–63 characters. It starts with a lowercase letter and then contains lowercase letters, digits, or single hyphens. `demo-api` is valid. `Demo`, `1demo`, `demo--api`, and `demo-` are not. Surrounding whitespace is trimmed before this check.
- **repo_url** must be `https://github.com/<owner>/<repo>` (a trailing `.git` or slash is accepted and removed). The host must be `github.com`. Query strings, fragments, ports, userinfo, and other hosts are rejected. Owner and repository keep their original case.

Duplicate names return `409`. A valid name that is not registered returns `404`.

The in-memory store also keeps deployment records (`CreateDeployment`, `GetDeployment`, `ListDeployments`) behind the `store.Store` interface. M0 does not expose deployments over HTTP and does not run them. Restarting `shipyardd` drops all data.

### Docker

The `shipyardd` image is a multi-stage build. The runtime stage is `gcr.io/distroless/static-debian12:nonroot`, so the process runs as the distroless `nonroot` user and listens on port 8080.

```bash
docker build -t shipyardd:dev .
docker run --rm -p 8080:8080 shipyardd:dev
```

### Tests and lint

```bash
make test
make lint
```

`make test` runs `go test -race` with an atomic coverage profile. `make lint` runs `go vet` and golangci-lint v1.61.0. Install that version so local lint matches CI:

```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/v1.61.0/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v1.61.0
```

GitHub Actions runs vet, golangci-lint, `go test -race` with coverage, and `docker build`.

## Layout

- `cmd/shipyardd` — HTTP API server
- `cmd/shipyard` — CLI (`apps create`, `apps list`, `apps get`)
- `internal/api` — routing, handlers, and input validation (`net/http`, `log/slog`)
- `internal/store` — `Store` interface and the in-memory app and deployment store
- `internal/config` — `SHIPYARD_ADDR` and the default CLI base URL

## License

MIT. See [LICENSE](LICENSE).
