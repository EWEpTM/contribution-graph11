# GitLab Import Agent

This agent backfills your contribution graph with your GitLab history using the GitLab REST API.

## Setup

### 1. Create a GitLab Personal Access Token

1. Go to your GitLab instance settings:
   - **gitlab.com**: https://gitlab.com/-/user_settings/personal_access_tokens
   - **Custom host** (e.g., git.example.com): https://git.example.com/-/user_settings/personal_access_tokens

2. Create a new token with at least the `read_api` scope
3. Copy the token value

### 2. Configure Environment Variables

```bash
export GITLAB_USERNAME=your-username
export GITLAB_TOKEN=your-personal-access-token
export GITLAB_HOST=git.example.com  # (optional, defaults to gitlab.com)
export SERVER_URL=http://localhost:8080/api/contributions  # (optional)
```

Or add to `.env` file:

```env
GITLAB_USERNAME=tomer
GITLAB_TOKEN=glpat-xxxxxxxxxxxxxx
GITLAB_HOST=git.elsc.ai-archive.io
SERVER_URL=http://127.0.0.1:5012/api/contributions
```

### 3. Run the Agent

```bash
# Test with dry run (no data uploaded)
DRY_RUN=true go run main.go

# Actually import your GitLab contributions
go run main.go
```

## How It Works

The agent:
1. Authenticates with GitLab using your personal access token
2. Fetches your user ID by username
3. Queries your events/contributions from the last year
4. Groups contributions by date
5. Creates individual events for each contribution (for proper heatmap intensity)
6. Pushes the data to your contribution-graph server

## Supported Features

- **Self-hosted GitLab** - Works with any GitLab instance (configure via `GITLAB_HOST`)
- **Event filtering** - Only imports "created" events to avoid duplication
- **Smart pagination** - Handles paginated API responses up to 100 pages
- **Dry run mode** - Test without uploading via `DRY_RUN=true`

## Metadata

Each imported contribution includes:
- `imported_from_gitlab` - Boolean flag
- `original_count` - Number of contributions on that day
- `import_date` - When the import was run
- `gitlab_host` - Which GitLab instance it came from

## Troubleshooting

### "User not found"
- Verify `GITLAB_USERNAME` matches your actual GitLab username
- Check token has proper permissions

### "GitLab API returned status 401"
- Verify your token is valid and hasn't expired
- Ensure token has `api` or `read_api` scope

### "Connection refused"
- For self-hosted GitLab: verify `GITLAB_HOST` is correct and accessible
- Check your network/firewall settings

## Scheduling (Optional)

### Linux (systemd timer)

Create `/etc/systemd/system/gitlab-import.service`:

```ini
[Unit]
Description=GitLab Contribution Import
After=network-online.target

[Service]
Type=oneshot
User=your-user
WorkingDirectory=/path/to/contribution-graph/agents/gitlab-import
EnvironmentFile=/path/to/.env
ExecStart=/usr/bin/go run main.go
```

Create `/etc/systemd/system/gitlab-import.timer`:

```ini
[Unit]
Description=Run GitLab Import Daily
Requires=gitlab-import.service

[Timer]
OnCalendar=daily
OnBootSec=1min

[Install]
WantedBy=timers.target
```

Enable and start:

```bash
sudo systemctl enable gitlab-import.timer
sudo systemctl start gitlab-import.timer
```
