# Shipyard

Shipyard is a small platform-as-a-service. Give it a GitHub repository and it builds a container image. Later milestones run that image. The project is a learning portfolio: a Go control plane, a local runtime, then a single AWS path. It is not a general-purpose cloud.

Milestone M0 is the control plane skeleton: `shipyardd` is an HTTP API and `shipyard` is a CLI. They register applications in an in-memory store.

Milestone M1 builds a Docker image from a registered repository. It clones the repo, requires a Dockerfile at the repository root, and tags the image `shipyard/<app>:<short-sha>`. It does not start containers, route traffic, or call AWS.

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

Install Go 1.22 or newer. Image builds also need `git` and `docker` on `PATH`.

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

Building an image requires a Dockerfile at the root of the registered repository. [`examples/hello`](examples/hello) is a tiny nginx app used by the integration test; point an app at a GitHub repository that has the same layout, then:

```bash
./bin/shipyard deploy hello --ref main
./bin/shipyard deployments list hello
./bin/shipyard logs dep_1 --follow
```

The CLI talks to `http://127.0.0.1:8080` unless you pass `--api-url` or set `SHIPYARD_API_URL`. Put `--api-url` before the command.

`apps create` and `apps get` print one app as indented JSON. `apps list` prints `{"apps":[...]}`. `deploy` prints the new deployment. `deployments list` prints `{"deployments":[...]}`. `logs` prints the captured build log as plain text. `created_at` is UTC. Names are stored trimmed, and repository URLs are stored in canonical form (`https://github.com/<owner>/<repo>`, without a trailing `.git`).

### API

| Method and path | Behavior |
| --- | --- |
| `GET /healthz` | `{"status":"ok"}` |
| `POST /apps` | Register an app. Body: `{"name":"...","repo_url":"..."}`. `201` with the app, `Location: /apps/<name>`. |
| `GET /apps` | List apps in creation order: `{"apps":[...]}`. |
| `GET /apps/{name}` | Fetch one app. |
| `POST /apps/{name}/deployments` | Queue an image build. Body may be empty or `{"ref":"..."}`. `202` with the deployment, `Location: /deployments/<id>`. |
| `GET /apps/{name}/deployments` | List builds for one app in creation order: `{"deployments":[...]}`. |
| `GET /deployments/{id}` | Fetch one deployment. |
| `GET /deployments/{id}/logs` | Captured build log as `text/plain`. `?follow=1` keeps the stream open until the build finishes. |

`POST` routes require `Content-Type: application/json` when a body is present. `POST /apps/{name}/deployments` also accepts an empty body with no content type, which selects the repository default branch. Unknown JSON fields are rejected. Errors from these routes are JSON: `{"error":"..."}`. A path that matches no route returns net/http's plain-text 404. A known path with the wrong method returns 405 and an `Allow` header.

Validation:

- **name** is a slug of 1–63 characters. It starts with a lowercase letter and then contains lowercase letters, digits, or single hyphens. `demo-api` is valid. `Demo`, `1demo`, `demo--api`, and `demo-` are not. Surrounding whitespace is trimmed before this check.
- **repo_url** must be `https://github.com/<owner>/<repo>` (a trailing `.git` or slash is accepted and removed). The host must be `github.com`. Query strings, fragments, ports, userinfo, and other hosts are rejected. Owner and repository keep their original case.
- **ref** is optional. Empty means the default branch. Otherwise it is a branch, tag, or commit SHA of at most 255 characters, using letters, digits, `.`, `_`, `-`, and `/`. It cannot start with `-`, contain `..` or `//`, or end with `/`, `.`, or `.lock`.
- **deployment id** in URLs must look like `dep_1`.

Duplicate names return `409`. A valid name that is not registered returns `404`. A full build queue returns `503`.

Deployment records contain `id`, `app_name`, `ref`, `commit_sha`, `status`, `image_tag`, `error`, `created_at`, and, once a worker starts, `started_at` and `finished_at`. Status is `queued`, `building`, `succeeded`, or `failed`. The image tag is `shipyard/<app>:<12 hex chars of the commit>`. Restarting `shipyardd` drops apps, deployments, and logs.

The M0 store helper `CreateDeployment` can still record a `pending` placeholder. The build pipeline does not use that status.

### Builds

`POST /apps/{name}/deployments` stores a `queued` deployment and returns immediately. A fixed pool of workers (default 2) pulls jobs from a bounded queue (default 16). A worker:

1. Marks the deployment `building`.
2. Shallow-clones the app's GitHub URL at the requested ref into a temp directory. An empty ref fetches the remote `HEAD`.
3. Requires a regular `Dockerfile` at the repository root. A missing file fails the deployment with `no Dockerfile at repository root`.
4. Runs `docker build` and tags `shipyard/<app>:<short-sha>`.
5. Marks the deployment `succeeded` or `failed`, stores the error string, and deletes the temp directory.

The whole job uses one timeout (default 10m). The clone uses a shorter timeout (default 2m) inside that budget. Canceling the server context, or stopping the process, cancels queued and in-flight builds. Build output is appended to an in-memory log of at most 1 MiB. `GET /deployments/{id}/logs` returns what has been captured. With `?follow=1` it flushes new chunks until the status is terminal. `shipyard logs --follow` uses that mode and exits 1 when the deployment did not succeed.

`shipyardd` settings, overridable by flag or environment:

| Setting | Flag | Environment | Default |
| --- | --- | --- | --- |
| Listen address | `--addr` | `SHIPYARD_ADDR` | `:8080` |
| Workers | `--build-concurrency` | `SHIPYARD_BUILD_CONCURRENCY` | `2` (1–32) |
| Queue length | `--build-queue-size` | `SHIPYARD_BUILD_QUEUE_SIZE` | `16` (1–256) |
| Build timeout | `--build-timeout` | `SHIPYARD_BUILD_TIMEOUT` | `10m` (1s–30m) |
| Clone timeout | `--clone-timeout` | `SHIPYARD_CLONE_TIMEOUT` | `2m`, and not longer than the build timeout |
| Clone size cap | `--clone-max-bytes` | `SHIPYARD_CLONE_MAX_BYTES` | `104857600` (100 MiB, including `.git`) |

### Architecture

```
shipyard CLI
    |
    v
shipyardd  -- net/http routes --> in-memory store
    |
    +--> Runner (bounded worker pool)
            |
            +--> source.Cloner   git fetch --depth 1
            +--> build.Builder   docker build
```

Clone and image build sit behind interfaces so tests can replace both:

- `source.Cloner` (`internal/source`). `Git` shells out to `git`. `Fake` writes a fixture tree.
- `build.Builder` (`internal/build`). `Docker` shells out to `docker build`. `Fake` records the tag and returns a scripted result.
- `deploy.Runner` (`internal/deploy`) owns the queue, timeouts, status transitions, temp directories, and log capture.

The Docker builder calls the `docker` CLI instead of the Docker Engine SDK. Current Docker installs build with BuildKit, and `docker build` is that builder. The Engine SDK `ImageBuild` call is the legacy builder; driving BuildKit through the SDK needs a session client and a much larger module graph. Shipyard has no third-party modules. The CLI is the same command a person would run, with the context directory and tag passed as exec arguments.

`git` and `docker` run as subprocesses, never through a shell. On Unix each child is its own process group and is killed when the build context ends, so a timed-out fetch does not leave `git-remote-https` behind. The git environment drops inherited `GIT_*` variables and disables credential prompts.

Security limits for M1:

- Repository URLs are re-checked at clone time and must still be `https://github.com/<owner>/<repo>`.
- Refs are a single git argument from a small character set, so they cannot be parsed as options (`--upload-pack=...`) or refspecs.
- Checkout size and clone time are capped. The temp directory is removed on success, failure, and panic.
- A symlink that points outside the checkout fails the build before `docker build` runs. The Dockerfile itself must be a regular file.
- The image tag passed to docker is only `shipyard/<slug>:<hex>`.

The distroless `shipyardd` image below is only the API process. It does not contain `git` or `docker`. Builds run when `shipyardd` is a normal host process (`make run` or `./bin/shipyardd`) on a machine where those binaries are installed.

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
make test-integration   # needs Docker; builds examples/hello
```

`make test` runs `go test -race` with an atomic coverage profile. The Docker integration test stays skipped unless `SHIPYARD_INTEGRATION=1`. `make lint` runs `go vet` and golangci-lint v1.61.0. Install that version so local lint matches CI:

```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/v1.61.0/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v1.61.0
```

GitHub Actions runs vet, golangci-lint, `go test -race` with coverage, the integration test against [`examples/hello`](examples/hello), and `docker build` of the `shipyardd` image. The integration test builds `shipyard/hello:0123456789ab` and inspects the image. It does not run a container.

## Layout

- `cmd/shipyardd` — HTTP API server and build worker pool
- `cmd/shipyard` — CLI (`apps`, `deploy`, `deployments list`, `logs`)
- `internal/api` — routing, handlers, and input validation (`net/http`, `log/slog`)
- `internal/deploy` — build queue, status transitions, timeouts, and log capture
- `internal/source` — `Cloner` interface, `git` implementation, and test fake
- `internal/build` — `Builder` interface, `docker build` implementation, and test fake
- `internal/store` — `Store` interface and the in-memory app, deployment, and log store
- `internal/config` — listen address, API URL, and build limits
- `examples/hello` — static nginx app built by the integration test

## License

MIT. See [LICENSE](LICENSE).
