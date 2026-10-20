# noroshi Helm chart

Installs [noroshi](https://github.com/dor1a/noroshi): health checks, a status page, and incident timelines in one Deployment backed by PostgreSQL.

## Install

noroshi needs a PostgreSQL database and, for the admin pages, an OIDC client. Put the secrets in a Secret first; the chart only reads it.

```sh
kubectl create secret generic noroshi \
  --from-literal=database-url='postgres://noroshi:...@postgres:5432/noroshi' \
  --from-literal=oidc-client-secret='...' \
  --from-literal=session-key="$(openssl rand -hex 32)"

helm install noroshi oci://ghcr.io/dor1a/charts/noroshi --version 0.1.0 -f values.yaml
```

A minimal `values.yaml`:

```yaml
baseURL: https://status.example.com
adminURL: https://status-admin.example.com
oidc:
  issuer: https://sso.example.com/realms/main
  clientID: noroshi
secret:
  name: noroshi
config:
  pages:
    - key: home
      title: Service Status
      timezone: Asia/Seoul
      groups:
        - key: web
          name: Web
          components:
            - key: website
              name: Website
              checks:
                - key: https
                  type: http
                  url: https://example.com/
```

`config` is the noroshi config file as it is ([reference](https://github.com/dor1a/noroshi#config-file)). A change to it rolls the pods, because noroshi reads the file at startup.

## Values

| Key | Default | Notes |
| -- | -- | -- |
| `replicaCount` | `1` | Replicas share the database; a Postgres advisory lock picks the one that runs checks. A PodDisruptionBudget is added above 1 |
| `image.repository` | `ghcr.io/dor1a/noroshi` | |
| `image.tag` | appVersion | |
| `image.digest` | | `sha256:...`. Replaces the tag when set |
| `baseURL`, `adminURL` | | Absolute URLs users reach. `adminURL` is the OIDC redirect base |
| `oidc.*` | | `issuer`, `clientID`, `adminGroup` (`noroshi-admins`), `groupsClaim` (`groups`), `scopes` |
| `locale` | `en` | Default UI language, and the language of names in metrics |
| `theme`, `logLevel` | | `NOROSHI_THEME`, `NOROSHI_LOG_LEVEL` |
| `secret.name` | | Required. Existing Secret with the keys below |
| `secret.env` | database URL, OIDC client secret, session key | Variable name to Secret key. Each becomes `NAME_FILE` pointing at the mounted key. Add webhook variables named by `url_env`; set one to `null` to drop it |
| `extraEnv` | `[]` | More environment variables, for example `NOROSHI_DEV_NO_AUTH` |
| `config` | one example page | The noroshi config file |
| `existingConfigMap` | | Use a ConfigMap with a `config.yaml` key instead of `config` |
| `service.public`, `service.admin` | ClusterIP, port 80 | `type`, `port`, `annotations` |
| `service.ops.port` | `8082` | `/healthz`, `/readyz`, `/metrics`. Keep it inside the cluster |
| `httpRoute.public`, `httpRoute.admin` | off | Gateway API routes: `parentRefs`, `hostnames`, `annotations` |
| `ingress.public`, `ingress.admin` | off | `className`, `hosts`, `tls`, `annotations` |
| `serviceMonitor.enabled` | `false` | Scrapes the ops port. `labels` for your Prometheus selector |
| `prometheusRule.enabled` | `false` | The rules from `deploy/prometheus/rules.yaml`. `labels` for your rule selector |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `priorityClassName` | | Usual pod settings |
| `extraVolumes`, `extraVolumeMounts` | `[]` | For example a CA bundle with `SSL_CERT_FILE` in `extraEnv` |

The pod runs as user 65532 with a read-only root file system, no capabilities, and the RuntimeDefault seccomp profile, which passes the `restricted` Pod Security Standard. Secrets are mounted at `/run/secrets/noroshi` and the config at `/etc/noroshi`.

## Alerts

With `serviceMonitor.enabled` and `prometheusRule.enabled`, the Prometheus Operator scrapes noroshi and loads its rules. Routing and message templates belong to Alertmanager; [deploy/alertmanager](https://github.com/dor1a/noroshi/tree/main/deploy/alertmanager) has examples.

## Versions

The chart is versioned apart from the app. A `chart-X.Y.Z` tag publishes chart X.Y.Z, and its `appVersion` is the image installed unless `image.tag` or `image.digest` is set.
