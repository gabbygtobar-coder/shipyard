-- Durable apps, deployments, logs, releases, and webhook deliveries.

CREATE TABLE apps (
    name TEXT PRIMARY KEY,
    repo_url TEXT NOT NULL,
    branch TEXT NOT NULL,
    auto_deploy INTEGER NOT NULL,
    webhook_secret TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE TABLE deployments (
    id TEXT PRIMARY KEY,
    app_name TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    image_tag TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    rollback_of TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    FOREIGN KEY (app_name) REFERENCES apps(name)
);

CREATE INDEX idx_deployments_app ON deployments(app_name);

CREATE TABLE deployment_logs (
    deployment_id TEXT PRIMARY KEY,
    data BLOB NOT NULL,
    truncated INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (deployment_id) REFERENCES deployments(id)
);

CREATE TABLE releases (
    app_name TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    image_tag TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (app_name, deployment_id),
    FOREIGN KEY (app_name) REFERENCES apps(name)
);

CREATE INDEX idx_releases_app ON releases(app_name);

CREATE TABLE deliveries (
    id TEXT PRIMARY KEY,
    received_at TEXT NOT NULL
);

CREATE TABLE counters (
    name TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);
