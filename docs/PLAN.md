# noroshi plan

> Status: phases 1, 2 and 4 are done. Phases 1 and 2 were verified locally (2026-10-13), and the Helm chart has run with two replicas on a k3s cluster in daily use since 2026-10-20 (v0.2.0). Items marked `pending` were built with the draft values in this document.
> The thresholds (failures in a row, successes to recover, warning window) can be changed in YAML. Changing how statuses roll up or how uptime is counted means editing the code (`internal/judge`, `internal/uptime`) and its test tables.

## Summary

noroshi is a self-made status page that puts health checks, a public status page, and incident records into one binary.

| Item | Detail |
| -- | -- |
| First environment | Homelab (docker compose, single host) |
| Second environment | Kubernetes, installed with the Helm chart (an internal page for a whole organisation) |
| Shape | One Go binary, one image, Postgres |
| UI | HTMX with server rendering, English by default with Korean |
| License | MIT |

See [architecture.md](architecture.md) for the diagrams.

## Why

A status page should carry incidents as well as checks.

- An incident builds a timeline (Investigating → Identified → Monitoring → Resolved) rather than a single banner
- Past incidents are kept
- Incidents are tied to the components they affect
- Scheduled maintenance and subscriptions are first-class
- Settings live in YAML, so they can be managed with GitOps

## Design principles

1. **The app stays generic; environment differences go into settings.** No homelab-only features in the core (a particular SSO product, a particular CDN API).
2. **Run checks, but build no operator alerting.** Results go out on `/metrics`; the existing Prometheus and Alertmanager handle alerts.
3. **Structure is code, records are data.** Pages, components, and checks are defined in YAML (Git). Only incidents and check results accumulate in the database.
4. **The public page is read-only and separate from admin functions.** Each gets its own listener.
5. **Keep dependencies small.** Mostly the standard library. Static files (HTMX, CSS) are embedded in the binary. No CDN: a status page should not stop rendering because someone else's CDN is down.

## Layout

```
            YAML (Git): pages, components, checks
                     │
┌──────────── noroshi (one Go binary) ─────────────────────────────┐
│ prober   runs on one instance only (Postgres advisory lock)      │
│   HTTP(S) / TCP / TLS expiry / domain expiry                     │
│     → check state → component status                             │
│                                                                  │
│ public :8080   read-only, no sign-in                             │
│ admin  :8081   OIDC sign-in, incident writing                    │
│ ops    :8082   /healthz /readyz /metrics (internal only)         │
└──────────┬─────────────────────────────────────┬─────────────────┘
       Postgres                              Prometheus
  (check results, status history,                │
   incidents)                         Alertmanager → existing channels
```

| Target | Homelab | Kubernetes (example stack) |
| -- | -- | -- |
| Runtime | One compose service | Deployment (several public replicas are fine; only the leader runs checks) |
| Public | Reverse proxy → public listener | Service and route → public listener (internal network) |
| Admin | Reverse proxy → admin listener | Internal route → admin listener |
| Sign-in | OIDC provider (authentik) | OIDC provider (Keycloak) |
| Database | Existing Postgres | CloudNativePG |
| Secrets | Host files → `*_FILE` | Secret (ESO) → `*_FILE` |
| Scraping | Prometheus scrape config → ops listener | ServiceMonitor → ops listener |

## Settings

Every setting comes from an environment variable. Secrets can come from a file named by the `*_FILE` variable, which wins when both are set.

| Variable | Example | Notes |
| -- | -- | -- |
| `NOROSHI_CONFIG` | `/etc/noroshi/config.yaml` | Pages, components, and checks |
| `NOROSHI_DATABASE_URL(_FILE)` | `postgres://...` | |
| `NOROSHI_PUBLIC_ADDR` | `:8080` | |
| `NOROSHI_ADMIN_ADDR` | `:8081` | Everything except the sign-in flow needs a session, so it can sit behind a public hostname |
| `NOROSHI_OPS_ADDR` | `:8082` | Keep it internal: `/metrics` names every check target |
| `NOROSHI_BASE_URL` | `https://status.example.com` | Used to build public links |
| `NOROSHI_ADMIN_URL` | `https://status-admin.example.com` | OIDC redirect URI |
| `NOROSHI_OIDC_ISSUER` | | |
| `NOROSHI_OIDC_CLIENT_ID` | | |
| `NOROSHI_OIDC_CLIENT_SECRET(_FILE)` | | |
| `NOROSHI_OIDC_ADMIN_GROUP` | `noroshi-admins` | Admin when the groups claim contains this value |
| `NOROSHI_SESSION_KEY(_FILE)` | | Signs the session cookie |
| `NOROSHI_THEME` | `tokyonight` | Colour theme: `tokyonight` (Catppuccin Latte when the system is light), `ink` (follows the system), `dancheong`, `klein`. Visitors can try others with `?theme=` |
| `NOROSHI_DEFAULT_LOCALE` | `en` | `en` or `ko`. Used when `?lang=`, the cookie, and the browser language all give nothing |

YAML example (`pending`: schema not final):

```yaml
pages:
  - key: home
    title:
      ko: 서비스 상태
      en: Service Status
    timezone: Asia/Seoul
    groups:
      - key: public
        name:
          ko: 공개 서비스
          en: Public services
        components:
          - key: blog
            name:
              ko: 블로그
              en: Blog
            checks:
              - key: https
                type: http
                url: https://example.com/
                expect_status: 200
                interval: 60s
                timeout: 10s
              - key: domain
                type: domain_expiry
                domain: example.com
                interval: 12h
                warn_before: 30d
```

- `key` is the stable ID that ties the YAML to the database. Renaming or reordering keeps the history.
- Items removed from the YAML are archived, not deleted, so past uptime and incidents stay.
- Check targets are defined only in YAML. The admin pages never take a URL, so noroshi cannot be turned into a tool for probing the internal network.

## Checks

| Type | Phase 1 | What it checks |
| -- | -- | -- |
| `http` | Done | Status code, optional body keyword, response time |
| `tcp` | Done | The connection succeeds |
| `tls` | Done | Certificate time left |
| `domain_expiry` | Done | Domain expiry date over RDAP |
| `push` | Later | A signal an outside job sends on a schedule (cron watching) |
| `dns` | Later | |
| ICMP | No | Needs `NET_RAW` in the container, which the Kubernetes restricted policy blocks |
| `docker` | Done (phase 2) | Container running and healthy, through a read-only socket proxy. Added for the homelab's container monitors; reverses the earlier "no container checks" |

### Judgement rules (`pending`)

A draft. Table-driven tests pin it down first, and the code is written to pass them.

| Rule | Draft |
| -- | -- |
| Failure | Timeout, connection failure, unexpected status code, missing keyword |
| Slow | Response time over `slow_after` (per check, no default) makes it `degraded` |
| Down | 3 failures in a row (`fail_threshold`) |
| Recovered | 2 successes in a row (`recover_threshold`) |
| Expiry warning | A certificate or domain inside `warn_before` shows only in metrics. The public status does not change |
| Expired | An already expired certificate counts as a failure |
| Component status | All checks down → `major_outage`, some down → `partial_outage`, slow → `degraded_performance`, otherwise `operational` |
| Manual status | An open incident can set a component's status (phase 2). While it is open, it wins over the check result |

The statuses are `operational`, `degraded_performance`, `partial_outage`, `major_outage`, and `under_maintenance`.

### Leader election

- Only the instance holding `pg_try_advisory_lock` on its own database connection runs the prober.
- If that connection drops, the lock goes with it and another instance takes over.
- The same code works in the homelab (one instance) and on Kubernetes (several). The Kubernetes Lease API is not used.

## Uptime (`pending`)

The 90-day bars and percentages on the public page follow these rules.

| Rule | Draft |
| -- | -- |
| Unit | Duration in the component's status history |
| Counts as down | All of `major_outage` and `partial_outage` (`pending`: whether partial should count only in part) |
| Counts as up | `operational`, `degraded_performance` |
| Left out | `under_maintenance`, no data |
| No data | Grey bar, left out of the percentage, never counted as down |
| Days | Cut in the page's `timezone` |

## Data model (draft)

| Table | Main fields | Written by |
| -- | -- | -- |
| `page` | key, title (ko/en), timezone | YAML |
| `component_group` | page, key, name, position | YAML |
| `component` | page, group, key, name, description, position, archived_at | YAML |
| `checks` | component, key, type, spec (jsonb), archived_at | YAML |
| `check_result` | check, at, ok, latency_ms, detail | prober (kept 14 days) |
| `check_state` | check, state, since, consecutive_fail, consecutive_ok | prober (current values) |
| `component_status_event` | component, status, at, source | prober (kept for good) |
| `prober_session` | instance, started_at, last_seen_at | prober (kept for good). Time outside these sessions counts as no data |
| `incident` | page, title, stage, started_at, resolved_at, created_by. There is no separate impact field; it is the worst of the component statuses | admins (phase 2) |
| `incident_update` | incident, stage, body, at, author | admins (phase 2) |
| `incident_component` | incident, component, status, since, until. The status of an open interval replaces the check result and counts toward uptime | admins (phase 2) |
| `notification` | update, notifier, ok, attempts, detail, at. Webhook URLs are not stored | announcements (phase 2) |

Migration SQL is embedded in the binary and applied at startup. Daily uptime is not stored; it is computed from `component_status_event`, `incident_component`, and `prober_session` when a page is drawn (cached for 30 seconds).

## Metrics (`pending`: names not final)

Alert rules depend on these names, so they do not change once set.

| Name | Labels | Meaning |
| -- | -- | -- |
| `noroshi_page_info` | page, title, url | Always 1. Page title and address for alert messages; every instance reports it |
| `noroshi_check_up` | page, component, check, type, alert | Last verdict (1 up, 0 down) |
| `noroshi_check_info` | page, component, check, name, target | Always 1. Display name and what the check probes, for alert messages |
| `noroshi_check_failure_info` | page, component, check, reason | Always 1. Latest failure, kept until the check is up again |
| `noroshi_check_duration_seconds` | page, component, check | Last response time |
| `noroshi_check_last_run_timestamp_seconds` | page, component, check | Last run, to catch a stalled check |
| `noroshi_check_interval_seconds` | page, component, check | Configured interval, used by the stall rule |
| `noroshi_cert_expiry_timestamp_seconds` | page, component, check, alert | Certificate expiry |
| `noroshi_domain_expiry_timestamp_seconds` | page, component, check, alert | Domain expiry |
| `noroshi_component_status` | page, component, status | 1 for the current status |
| `noroshi_prober_leader` | | 1 when this instance is the leader |

Example alert rules ship in the repo (`deploy/prometheus/rules.yaml`, also loaded by the chart's PrometheusRule), with example Alertmanager routes and a Discord and Microsoft Teams template in `deploy/alertmanager/`.

## Screens

| Screen | Phase 1 | Phase 2 |
| -- | -- | -- |
| Public: overall banner, components by group, 90-day bars | Done | |
| Public: open incidents, past incidents | | Done |
| Admin: sign-in, current check states and recent results | Done | |
| Admin: incident writing with a timeline, manual status | | Done |

- The language comes from `?lang=`, then the cookie, `Accept-Language`, and `NOROSHI_DEFAULT_LOCALE`.
- Times show in the page's `timezone`.
- The public page must read fine without JavaScript. HTMX only refreshes it.

## Phases

| Phase | Scope | Done when |
| -- | -- | -- |
| 1. Checks and status page | Checks, judgement, component status, public page, uptime, `/metrics`, admin sign-in | The homelab's checks, domain and certificate expiry included, run on noroshi with Prometheus alert rules in place |
| 2. Incidents | Incident timeline, past records, manual status, Teams and Discord announcements | One real outage is announced through noroshi and recorded until it is resolved |
| 3. Extensions | Scheduled maintenance, subscriptions (RSS, then mail), incident drafts from Alertmanager webhooks, static export, `push` checks | Decided item by item |
| 4. Kubernetes | Helm chart with ServiceMonitor, PrometheusRule, Gateway API and Ingress routes, probes; several replicas verified on k3s | Running on a cluster in daily use |

## Out of scope

- An operator alerting engine (deduplication, silences, escalation). Alertmanager does that
- ICMP checks
- Entering check targets in the admin pages
- SQLite
- Per-page admin rights (admins are one group)
- External CDNs, fonts, or scripts

## How it is built

- Builds and tests run in the `golang` container, so the host needs only Docker.
- Judgement and uptime rules get table-driven tests first. Those tables are what a person reviews.
- External dependency candidates: a Postgres driver, an OIDC client, the Prometheus client, a YAML parser. Anything beyond these needs a written reason.
- One small feature at a time, each finished before the next.

## Decisions left to a person

| Item | Where the draft is |
| -- | -- |
| YAML schema | Settings |
| Database design | Data model |
| Judgement rules | Checks → Judgement rules |
| Uptime rules | Uptime |
| Metric names | Metrics |
| Security review (OIDC, sessions, CSRF, check target limits) | After implementation |
