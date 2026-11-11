-- Append-only per-app event log.

CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    app_name TEXT NOT NULL,
    type TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    deployment_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    FOREIGN KEY (app_name) REFERENCES apps(name)
);

CREATE INDEX idx_events_app ON events(app_name, id);
