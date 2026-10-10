# Production deployment (noema.dixon.cx)

Noema runs as a Docker container supervised by systemd, talks to the host
PostgreSQL 16 cluster over the docker bridge, and sits behind nginx. Access
control is Noema's own sign-in page. Cloudflare proxies the hostname
(wildcard `*.dixon.cx`).

```
Cloudflare -> nginx :443 (TLS) -> 127.0.0.1:29630 -> container :8080 -> postgres 172.17.0.1:5432
```

## Deploying a new version

```sh
git pull
deploy/prod/build.sh
```

`build.sh` builds `noema:<git describe>` and `noema:latest`, then restarts the
`noema` service. Migrations are applied automatically when the daemon starts.
Roll back with `docker tag noema:<older-version> noema:latest && sudo systemctl restart noema`
(`docker images noema` lists the versions you have).

## Where things live

| What | Where |
|---|---|
| systemd unit | `/etc/systemd/system/noema.service` (source: `deploy/prod/noema.service`) |
| Environment and secrets | `/etc/noema/noema.env` (root, 0600): DB URL, master key, bootstrap admin |
| nginx vhost | `/etc/nginx/sites-enabled/noema.dixon.cx.conf` (source: `deploy/prod/noema.dixon.cx.conf`) |
| TLS certificate | `/etc/letsencrypt/live/noema.dixon.cx` (certbot, nginx authenticator, auto-renews) |
| Research folder | `/srv/noema/research` mounted at `/research` (owned by uid 10001) |
| Database | `noema` database, `noema` role on the host cluster; `vector` extension created by `postgres` |
| Logs | `journalctl -u noema -f` (JSON); nginx `/var/log/nginx/noema_*.log` |

The daemon ignores proxy headers, so it sees every visitor as the docker
gateway address: the login rate limit (20/min) is shared by everyone, and the
per-account lockout is what actually stops password guessing. Use a strong
admin password and enable TOTP on the account page.

**Back up `NOEMA_MASTER_KEY`.** It encrypts provider API keys stored in the
database; losing it means re-entering every key.

## Common tasks

```sh
# noemactl against the running instance
docker exec -it noema noemactl --help

# psql as the app (from the host use the postgres superuser instead)
sudo -u postgres psql noema
```

## Rebuilding from scratch

Applies to a fresh host; everything below was done once on this one.

```sh
sudo -u postgres psql -c "CREATE ROLE noema LOGIN PASSWORD '...'" -c "CREATE DATABASE noema OWNER noema"
sudo -u postgres psql -d noema -c "CREATE EXTENSION vector"   # needs superuser; migration 0005 expects it
# pg_hba.conf must allow 172.17.0.0/16 and postgresql.conf must listen on 172.17.0.1

sudo install -d -m 700 /etc/noema && sudo install -d -o 10001 -g 10001 /srv/noema/research
sudoedit /etc/noema/noema.env   # keys as in .env.example, plus:
#   NOEMA_ENV=production  NOEMA_HTTP_ADDR=0.0.0.0:8080  NOEMA_RESEARCH_DIR=/research
#   NOEMA_DATABASE_URL=postgres://noema:PASS@172.17.0.1:5432/noema?sslmode=disable
#   NOEMA_MASTER_KEY=$(openssl rand -base64 32)  NOEMA_BOOTSTRAP_ADMIN_PASSWORD=...

sudo cp deploy/prod/noema.service /etc/systemd/system/ && sudo systemctl daemon-reload
deploy/prod/build.sh && sudo systemctl enable noema

sudo certbot certonly --nginx -d noema.dixon.cx --key-type ecdsa
sudo cp deploy/prod/noema.dixon.cx.conf /etc/nginx/sites-enabled/ && sudo nginx -t && sudo systemctl reload nginx
```
