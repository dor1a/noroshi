<div align="center">

# noroshi

**Health checks, a public status page, and incident updates from one Go binary**

Built to replace Uptime Kuma in a homelab first, then run unchanged on Kubernetes.<br>
The UI is in English with a full Korean translation.

[![build](https://github.com/dor1a/noroshi/actions/workflows/build.yml/badge.svg)](https://github.com/dor1a/noroshi/actions/workflows/build.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![image](https://img.shields.io/badge/image-ghcr.io%2Fdor1a%2Fnoroshi-2496ED?logo=docker&logoColor=white)](https://github.com/dor1a/noroshi/pkgs/container/noroshi)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

</div>

> [!NOTE]
> Pre-release. Phases 1 and 2 of [the plan](docs/PLAN.md) are done: checks, the status page, and incidents. The config format may still change before 1.0. See [docs/architecture.md](docs/architecture.md) for how the pieces fit.

## What it does

- Runs `http`, `tcp`, `tls`, and `domain_expiry` (RDAP) checks defined in a YAML file.
- Debounces results: by default a check goes down after 3 failures in a row and comes back after 2 successes in a row.
- Rolls check states up into a component status and shows 90 days of daily uptime bars.
- Exports Prometheus metrics. noroshi sends no alerts itself; point Alertmanager at the metrics instead ([example rules](deploy/prometheus/rules.yaml)).
- Puts the admin pages behind any OIDC provider. Admin rights come from a group in the ID token.
- Lets admins write incidents with a timeline (investigating, identified, monitoring, resolved). A status set on a component in an open incident replaces what its checks say and counts toward uptime. Each update can be announced to Discord or Microsoft Teams.
- Lets several replicas share one database. A Postgres advisory lock picks the one instance that runs checks, and another takes over within about 10 seconds if it stops.

## Quick look

```sh
docker build -t noroshi:dev .
docker run --rm -i noroshi:dev validate /dev/stdin < deploy/config.example.yaml
```

To run it, you need Postgres and a config file. [deploy/compose/compose.yaml](deploy/compose/compose.yaml) shows a full setup with secrets read from files.

For a local trial without an identity provider, set `NOROSHI_DEV_NO_AUTH=1`. The admin pages are then open to anyone who can reach the admin port, so never set it on a shared network.

## Configuration

Settings come from environment variables. For secrets, set `NAME_FILE` to a file path instead of `NAME`; the file wins when both are set.

| Variable | Default | Notes |
| -- | -- | -- |
| `NOROSHI_CONFIG` | `/etc/noroshi/config.yaml` | Pages, components, and checks |
| `NOROSHI_DATABASE_URL` | | Required. Postgres connection string |
| `NOROSHI_PUBLIC_ADDR` | `:8080` | Status pages, no authentication |
| `NOROSHI_ADMIN_ADDR` | `:8081` | Admin pages and login. Safe to publish: every page needs a session |
| `NOROSHI_OPS_ADDR` | `:8082` | `/healthz`, `/readyz`, `/metrics`. Keep it internal, metrics name every check target |
| `NOROSHI_ADMIN_URL` | | Public URL of the admin listener, used for the OIDC redirect |
| `NOROSHI_OIDC_ISSUER` | | |
| `NOROSHI_OIDC_CLIENT_ID` | | |
| `NOROSHI_OIDC_CLIENT_SECRET` | | |
| `NOROSHI_OIDC_ADMIN_GROUP` | `noroshi-admins` | Required group for admin access |
| `NOROSHI_OIDC_GROUPS_CLAIM` | `groups` | Claim that lists the user's groups |
| `NOROSHI_OIDC_SCOPES` | `openid profile email` | Add a scope here if your provider only sends groups for it |
| `NOROSHI_SESSION_KEY` | | At least 32 bytes, signs the session cookie |
| `NOROSHI_DEFAULT_LOCALE` | `en` | `en` or `ko`. Used when neither `?lang=`, a cookie, nor the browser's `Accept-Language` picks one |
| `NOROSHI_THEME` | `tokyonight` | Colour theme: `tokyonight` (Catppuccin Latte when the system is in light mode), `ink` (also follows the system), `dancheong`, or `klein`. Visitors can switch with `?theme=` |
| `NOROSHI_DEV_NO_AUTH` | `false` | Turns off admin authentication |
| `NOROSHI_DEV_EMAIL` | | Email of the dev-mode user, to try `admin.gravatar` without a provider |
| `NOROSHI_LOG_LEVEL` | `info` | |
| `TZ` | `UTC` | Time zone for times on the admin pages. Pages use their own `timezone` |

The OIDC redirect URI to register with your provider is `<NOROSHI_ADMIN_URL>/oauth/callback`. The admin group is matched with or without a leading `/`, so Keycloak's full group paths work.

Signing out sends the browser to the provider's `end_session_endpoint` when discovery lists one, so the provider session ends too and the next visit asks for a login again. Register `<NOROSHI_ADMIN_URL>/signed-out` as a post-logout redirect URI to come back to noroshi afterwards. Whether the provider ends its own session depends on its logout settings: in authentik, give the provider an invalidation flow with a User Logout stage, such as `default-invalidation-flow`.

Outbound HTTPS checks use the system CA bundle. To trust a private CA, mount a bundle and set `SSL_CERT_FILE`.

### Config file

See [deploy/config.example.yaml](deploy/config.example.yaml). Every `key` is a stable ID that links the YAML to stored history, so renaming a component keeps its uptime as long as the key stays the same. Removing an item from the file archives it instead of deleting its history. noroshi reads the file at startup; restart it to apply changes.

| Check type | Fields |
| -- | -- |
| `http` | `url`, `method`, `expect_status` (codes or ranges such as `200-299`), `keyword`, `follow_redirects`, `slow_after`, `headers`, `insecure_skip_verify` |
| `tcp` | `address` (`host:port`) |
| `tls` | `address`, `server_name`, `insecure_skip_verify` |
| `domain_expiry` | `domain` |
| `docker` | `container` (name or ID), `docker_host` (`unix:///path` or `tcp://host:port`, default from `defaults.docker_host`, then `unix:///var/run/docker.sock`) |

All types accept `interval`, `timeout`, `fail_threshold`, `recover_threshold`, and `warn_before`. Durations take Go syntax plus days, for example `90s`, `12h`, or `30d`. `domain_expiry` checks default to a 12 hour interval.

On the admin pages, a check that answers slower than `slow_after` shows as degraded and its latency turns amber. Without `slow_after`, latency turns amber past half the timeout. It turns red from 80% of the timeout.

An expiring certificate or domain only shows up in metrics until it actually expires. A failed RDAP lookup leaves the check's state unchanged.

A `docker` check asks the Docker Engine API about one container: running is up, and when the container has a health check it must also be healthy (`starting` decides nothing). Point it at a read-only socket proxy that allows only `CONTAINERS`, not at the raw socket.

### Pages, logos, and alerts

Everything that shapes a page, and the admin pages, lives in the config file:

```yaml
admin:
  title: Phomelo
  logo: https://cdn.example.com/logo.png
  gravatar: true
pages:
  - key: default
    title: Internal page
    logo: https://cdn.example.com/logo.png   # fetched once at startup; also the favicon
    visibility: private     # only on the admin listener, after sign-in
  - key: public
    title: Phomelo
    logo: logo.png          # a file works too, relative to the config file
    alert: false            # already covered by container alerts elsewhere
    groups:
      - key: game
        name: Game
        components:
          - key: palworld
            name: Palworld
            checks:
              - key: container
                type: docker
                container: 90-game-palworld-1
```

| Field | Where | Effect |
| -- | -- | -- |
| `logo` | page, `admin` | An http(s) URL, fetched once at startup, or a file path relative to the config file. svg, png, jpg, webp, gif, or ico, up to 1 MiB. Shown next to the title and used as the favicon. If it cannot be loaded, noroshi logs a warning and shows its own mark |
| `title` | `admin` | Name in the admin masthead and browser tabs. Defaults to `noroshi` |
| `gravatar` | `admin` | `true` shows each admin's Gravatar, found by a SHA-256 hash of the email in the ID token. Browsers then load those images from gravatar.com. Off by default |
| `powered_by` | `footer` | `false` hides "Powered by noroshi" and the source link at the bottom of every page. On by default |
| `visibility` | page | `public` (default) or `private`. A private page returns 404 on the public listener and is listed at `/pages` on the admin listener |
| `alert` | page, group, component, check | `false` keeps the check on the page but sets `alert="false"` on its metrics, so the example rules do not page for it. Inner levels override outer ones |

### Incident announcements

Channels are declared once under `notifiers` and picked per page with `notify`. The webhook URL is a credential, so the file only names the variable that holds it; `NAME_FILE` works as for other secrets. noroshi refuses to start when a named variable is missing.

```yaml
notifiers:
  - key: teams
    type: teams            # or discord
    url_env: NOROSHI_WEBHOOK_TEAMS
    locale: ko             # labels in the message; defaults to NOROSHI_DEFAULT_LOCALE
pages:
  - key: home
    notify:
      - teams
```

| Type | Webhook | Notes |
| -- | -- | -- |
| `discord` | Channel settings, Integrations, Webhooks | Sent as an embed with `@` mentions turned off |
| `teams` | Channel, Workflows, "Send webhook alerts to a channel" | Sent as an Adaptive Card. A workflow belongs to the person who made it, so add a co-owner |

Delivery is retried on network errors, 429, and 5xx, three tries in all, and each result shows on the incident's admin page. Requests follow `HTTPS_PROXY`. Set `NOROSHI_BASE_URL` so messages link to the incident.

## Endpoints

| Listener | Path | Purpose |
| -- | -- | -- |
| public | `/` | The page, or a list when there are several |
| public | `/p/{key}` | One status page |
| public | `/p/{key}/incidents/{id}` | One incident and its timeline |
| public | `/api/v1/pages/{key}` | JSON summary with open incidents |
| admin | `/admin` | Check states and recent results |
| admin | `/admin/incidents` | Write and update incidents |
| admin | `/pages`, `/p/{key}` | Every page, private ones included |
| ops | `/healthz` | Process is up (also on public and admin) |
| ops | `/readyz` | Database is reachable |
| ops | `/metrics` | Prometheus metrics |

## Metrics

| Name | Labels |
| -- | -- |
| `noroshi_check_up` | page, component, check, type, alert |
| `noroshi_check_duration_seconds` | page, component, check |
| `noroshi_check_last_run_timestamp_seconds` | page, component, check |
| `noroshi_check_interval_seconds` | page, component, check |
| `noroshi_cert_expiry_timestamp_seconds` | page, component, check, alert |
| `noroshi_domain_expiry_timestamp_seconds` | page, component, check, alert |
| `noroshi_component_status` | page, component, status |
| `noroshi_prober_leader` | |

Only the leader reports check and component series. With several replicas, aggregate across instances in your alert rules.

## Development

Go runs in a container, so the host needs only Docker.

```sh
make test         # go test ./...
make vet
make validate     # check deploy/config.example.yaml
make image        # local architecture
make image-amd64  # for x86_64 hosts
make rules-check  # promtool on deploy/prometheus/rules.yaml
```

Two local stacks run the demo config in [deploy/config.demo.yaml](deploy/config.demo.yaml):

| Command | What runs | URLs |
| -- | -- | -- |
| `make dev` | Postgres and noroshi, admin sign-in off | public `localhost:18080`, admin `localhost:18081`, ops `localhost:18082` |
| `make e2e` | Postgres, authentik, noroshi with OIDC, Prometheus with the alert rules | public `localhost:28080`, admin `localhost:28081`, ops `localhost:28082`, authentik `localhost:9000`, Prometheus `localhost:29090` |

Both stacks also run a webhook sink that lists received announcements as JSON at `localhost:18090` (dev) or `localhost:28090` (e2e). To try a real channel, start the stack with `NOROSHI_DEV_DISCORD_URL` / `NOROSHI_DEV_TEAMS_URL` (or the `NOROSHI_E2E_` ones) set to a webhook URL. A sink path ending in `/fail` answers 400.

In the e2e stack, sign in as `e2e-admin` (member of `noroshi-admins`) or `e2e-viewer` (not a member, gets 403). Both use the password `e2e-password`. authentik takes about a minute to start. `make dev-down` and `make e2e-down` remove the containers and their data.

Work happens on `dev`, the default branch. `main` holds the latest release, and release tags are cut from it.

CI runs the tests on `main` and `dev` and builds images only for version tags. `vX.Y.Z` publishes `X.Y.Z`, `X.Y`, and `latest`; a pre-release such as `vX.Y.Z-rc.1` publishes only its own tag, which is handy for trying a build on a real host before releasing it.

Layout: `internal/judge` (state rules), `internal/uptime` (daily buckets), `internal/probe` (check runners), `internal/prober` (leader loop), `internal/store` (Postgres), `internal/web` (pages), `internal/auth` (OIDC).

## License

MIT. See [LICENSE](LICENSE).
