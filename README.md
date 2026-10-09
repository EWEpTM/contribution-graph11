# Open Contribution Graph

**A self-hosted, privacy-first server to visualize your life as a contribution heatmap.**

![Combined Dashboard View](docs/screenshot.png)

*Track coding, fitness, reading, meditation, and more — all in one dashboard.*

## ⚡ What is this?

GitHub tracks your code, but what about the rest of your work?

**Open Contribution Graph** aggregates "events" from any source — Git commits, fitness activities, books read — and renders them on a unified, GitHub-style heatmap.
It is **not** a time tracker. It is an **Event Tracker**. It answers the question: *"Did I show up today?"*

* **Universal:** If you can send a POST request, you can track it.
* **Private:** Self-hosted, single Go binary. Your data stays on your machine.
* **Fast:** PostgreSQL-backed, indexed queries, PWA offline support.
* **Multi-user:** Optional login + registration, per-user isolated data.

## ✨ Features

- **Per-source heatmaps** — switch between event types and view each habit individually.
- **Combine modes** — when viewing all sources together:
  - **热度 (Winner)** — the dominant source for each day determines the color.
  - **总计 (Stack)** — intensity based on total contributions that day.
- **Dynamic statistics** — total contributions, today's count, current streak, active source count.
- **Low-frequency mode** — a once-a-day habit lights up fully instead of fading.
- **Dark / light / system theme**, drag-to-reorder sources, installable PWA.
- **Multi-user** — admin manages users; each user only sees and tracks their own data.

## 🏗 Architecture

"Hub and Spoke": you run the server (Hub); "Agents" (Spokes) push JSON events to it.

1. **Server** — lightweight Go HTTP server on top of PostgreSQL.
2. **Agents** — scripts on your laptop / CI / phone that push events.
3. **Frontend** — dashboard using Apache ECharts, served as a PWA.

## 🚀 Quick Start (Docker)

```bash
docker-compose up -d
# open http://localhost:9999
```

The compose file starts PostgreSQL + the server and creates the admin account from
`AUTH_USERNAME` / `AUTH_PASSWORD`. Point it at your own volumes.

## 🚀 From Source

```bash
# requires a reachable PostgreSQL
export DATABASE_URL='postgres://user:pass@localhost:5432/db?sslmode=disable&TimeZone=Asia/Shanghai'
export AUTH_PASSWORD='changeme'   # unset = single-user mode, no login
go run ./cmd/server
```

### Configuration (environment variables)

| Variable        | Default          | Meaning                                             |
|-----------------|------------------|-----------------------------------------------------|
| `DATABASE_URL`  | — (required)     | PostgreSQL connection string.                       |
| `PORT`          | `8080`           | HTTP listen port.                                   |
| `TZ`            | `Asia/Shanghai`  | Business timezone used for day boundaries.          |
| `STATIC_DIR`    | `./static`       | Where the PWA frontend lives.                       |
| `AUTH_USERNAME` | `admin`          | Admin account name (seeded on first boot).          |
| `AUTH_PASSWORD` | —                | Set to enable login; unset = open single-user mode. |

## 🔌 Logging an event

POST a JSON array to `/api/contributions`:

```bash
curl -X POST http://localhost:8080/api/contributions -d '[{
  "source": "fitness",
  "context": "morning-run",
  "timestamp": "2026-10-09T07:00:00Z",
  "metadata": { "distance": "5km" }
}]'
```

`source`/`timestamp` are required; `timestamp` defaults to server time when omitted.
Duplicate events (same source+context+timestamp+user) are ignored on conflict.

### Included agents (`/agents`)

* **`git-watch`** (Go): scan local repos for commits and push them.
* **`github-import`** (Go): backfill your public GitHub history via the GraphQL API.
* **`gitlab-import`** (Go): backfill your GitLab activity via the REST API.

## 🛠 Tech Stack

* **Backend:** Go (1.21+)
* **Database:** PostgreSQL (via `jackc/pgx` stdlib driver, CGO-free)
* **Frontend:** HTML5 + Apache ECharts, Service Worker PWA

## License

**GPLv3** © Tomer Barak. Free software: redistribute and modify under the terms of the GNU GPL.
