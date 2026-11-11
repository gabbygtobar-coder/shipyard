# Recording a terminal and browser demo

This is the command list for a screen recording. It uses a public GitHub repository that contains [`examples/hello`](../examples/hello) at the repository root (`Dockerfile` and `index.html`). The page should include a version string so a rollback is visible. Two commits, or two tags, with different `VERSION` defaults are enough. The commands below call them `<commit-v1>` and `<commit-v2>`.

Replace `<owner>/<repo>` with that repository. Do not record the webhook secret if you set one. The dashboard token below is a demo value.

Expected machine: Docker is running, Go 1.22 or newer is installed, and ports 8080, 8081, 9090, and 3000 are free.

## Terminal

Record this shell. Leave the first `shipyardd` running.

```bash
cd "$(git rev-parse --show-toplevel)"
make build
export SHIPYARD_UI_TOKEN=dev-token
./bin/shipyardd
```

The listening log is JSON and includes `"ui":true`. In a second terminal, from the same directory:

```bash
export SHIPYARD_UI_TOKEN=dev-token
./bin/shipyard apps create --name hello --repo https://github.com/<owner>/<repo> --branch main
./bin/shipyard deploy hello --ref <commit-v1>
./bin/shipyard logs dep_1 --follow
```

`logs --follow` exits 0 when the deployment is `running`. Then:

```bash
curl -fsS http://127.0.0.1:8081/apps/hello/
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/metrics | head -n 40
./bin/shipyard events hello
```

You should see `hello from shipyard` (and the v1 version text), `{"status":"ok"}` from both health routes, Prometheus series starting with `shipyard_`, and a `deployed` event.

Deploy the second version and roll back:

```bash
./bin/shipyard deploy hello --ref <commit-v2>
./bin/shipyard logs dep_2 --follow
curl -fsS http://127.0.0.1:8081/apps/hello/
./bin/shipyard releases hello
./bin/shipyard rollback hello --to dep_1
./bin/shipyard logs dep_3 --follow
curl -fsS http://127.0.0.1:8081/apps/hello/
./bin/shipyard events hello
```

The proxy body is the v1 page again. The event list includes `rolled_back` for `dep_3`.

Optional, if the recording should show a failed health probe. This needs a second app whose health path never returns 2xx, and `shipyardd` started with a short threshold:

```bash
# restart shipyardd with:
# SHIPYARD_UI_TOKEN=dev-token SHIPYARD_MONITOR_THRESHOLD=2 SHIPYARD_MONITOR_INTERVAL=2s SHIPYARD_AUTO_RESTART=true ./bin/shipyardd
```

After two failed probes the app status becomes `unhealthy`, `shipyard events <app>` shows one `health_failed` row, and the local container is restarted once.

## Browser

1. Open `http://127.0.0.1:8080/ui`.
2. Sign in when prompted. Username: `shipyard`. Password: `dev-token`.
3. The list shows `hello`, status `running`, the public URL, and the current deployment.
4. Open `hello`. The page shows the repository, the current deployment, recent deployments with a `logs` link, the event timeline (`deployed`, then `rolled_back`), and a Rollback form.
5. Click the `logs` link for `dep_1`. The response is `text/plain` build output.
6. Open `http://127.0.0.1:8081/apps/hello/` in another tab. The body is the v1 page after the rollback.
7. On the app page, choose `dep_2` in the Release menu and press Roll back. Confirm the dialog. The page reloads with "Rollback queued." Follow `./bin/shipyard logs <new-id> --follow` in the terminal until it is `running`, then reload the proxy tab. The body is the v2 page.

The page reloads itself every few seconds while a deployment is `queued`, `building`, or `starting`.

## Prometheus and Grafana

Stop the host `shipyardd` first so port 8080 is free. From the repository root:

```bash
export SHIPYARD_UI_TOKEN=dev-token
docker compose --profile observability up --build
```

Wait until Prometheus and Grafana are up, create the app again through the CLI (`--api-url` is still `http://127.0.0.1:8080`), and deploy once. Then:

- Prometheus: `http://127.0.0.1:9090/graph?g0.expr=shipyard_active_deployments&g0.tab=0`
- Targets: `http://127.0.0.1:9090/targets` shows job `shipyard` as UP
- Grafana: `http://127.0.0.1:3000/d/shipyard/shipyard` (anonymous viewer). Panels: active deployments, build and deploy rates, build duration p95, proxy request rate, proxy latency p95, webhook deliveries

`docker compose up` without the profile does not start Prometheus or Grafana.

## What not to record

- `SHIPYARD_GITHUB_WEBHOOK_SECRET` or a real per-app webhook secret
- AWS keys, `terraform apply` output, or account ids
- `docker` socket errors from a daemon that is not running. Start Docker before the take.
