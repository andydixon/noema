# Installing Noema on Ubuntu

This guide installs the packages needed to build, test, and run Noema on a
64-bit Ubuntu host. Noema requires Go 1.26 or newer and PostgreSQL. pgvector is
optional for basic operation but required for semantic memory retrieval.

The recommended development setup runs PostgreSQL and pgvector with Docker
Compose. A native PostgreSQL installation is documented separately below. Do
not install both unless you deliberately want two database servers.

## Package summary

| Purpose | Packages or software | Required when |
|---|---|---|
| Base tools | `ca-certificates curl git make openssl tzdata` | Always |
| Local builds and race tests | Go 1.26+, `build-essential` | Building outside Docker |
| Container database | `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin` | Recommended Docker path |
| Native database | `postgresql-16 postgresql-client-16 postgresql-16-pgvector` | Native PostgreSQL path only |
| Development analyzers | `staticcheck gosec govulncheck` | Optional; needed by `make lint security` |

Node.js and npm are not required. The web interface is built into the Go
binaries and has no JavaScript package-manager step.

## 1. Install the base packages

```sh
sudo apt update
sudo apt install -y ca-certificates curl git make openssl tzdata build-essential
```

`build-essential` supplies the C compiler needed by Go's race detector. It may
be omitted on a runtime-only host receiving prebuilt Noema binaries.

## 2. Install Go

The Ubuntu archive may not provide a recent enough Go release. Install the
current Go 1.26 patch release from <https://go.dev/dl/> and follow the official
Linux instructions at <https://go.dev/doc/install>.

This repository's `go.mod` currently selects Go 1.26.1 and toolchain 1.26.8.
For an amd64 Ubuntu host, for example:

```sh
curl -LO https://go.dev/dl/go1.26.8.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.8.linux-amd64.tar.gz
```

Add Go to the shell path:

```sh
echo 'export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH' >> "$HOME/.profile"
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
go version
```

For arm64, use the `linux-arm64` archive instead. Never extract a new Go
release over an existing `/usr/local/go` tree.

## 3. Choose a database installation

### Option A: Docker Compose database (recommended)

The repository's Compose configuration uses `pgvector/pgvector:pg16`, so a
host PostgreSQL or pgvector package is not needed. Installing pgvector on the
host does not make it available inside a PostgreSQL container; the extension
must exist in the container image where the server runs.

Install Docker Engine from Docker's official apt repository. These commands
follow <https://docs.docker.com/engine/install/ubuntu/>:

```sh
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io \
  docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

Verify the installation:

```sh
sudo docker run --rm hello-world
docker compose version
```

Noema's Makefile invokes Docker without `sudo`. To permit that, add your user
to the `docker` group and then log out and back in:

```sh
sudo usermod -aG docker "$USER"
```

Membership in the `docker` group effectively grants root-level access to the
host. Use `sudo docker ...` instead and adjust the Makefile commands if that is
not acceptable for the machine.

Continue at [Configure and start Noema](#4-configure-and-start-noema).

### Option B: Native PostgreSQL 16 and pgvector

Use this path when PostgreSQL itself will run on the Ubuntu host. The pgvector
package major version must match the PostgreSQL server major version. Noema's
development configuration targets PostgreSQL 16.

The PostgreSQL Global Development Group apt repository provides both
PostgreSQL 16 and pgvector. Configure it using the project's official helper:

```sh
sudo apt install -y postgresql-common
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh
sudo apt update
sudo apt install -y postgresql-16 postgresql-client-16 \
  postgresql-16-pgvector
sudo systemctl enable --now postgresql
```

Repository documentation: <https://www.postgresql.org/download/linux/ubuntu/>
and <https://github.com/pgvector/pgvector#apt>.

Create the Noema role and database. Choose a strong password when prompted:

```sh
sudo -u postgres createuser --pwprompt noema
sudo -u postgres createdb --owner=noema noema
sudo -u postgres psql --dbname=noema \
  --command='CREATE EXTENSION IF NOT EXISTS vector;'
```

`CREATE EXTENSION` is required once in each database that uses vectors. It
does not require `shared_preload_libraries` or a PostgreSQL restart. Confirm
that the server can load it:

```sh
sudo -u postgres psql --dbname=noema \
  --command="SELECT extversion FROM pg_extension WHERE extname = 'vector';"
```

Enable the extension before running Noema migrations. Migration 0005 creates
the `memory_embeddings` table only when pgvector is available at migration
time.

## 4. Configure and start Noema

From the repository root:

```sh
cp .env.example .env
openssl rand -base64 32
```

Put the generated value in `NOEMA_MASTER_KEY` in `.env`, and set a strong
`NOEMA_BOOTSTRAP_ADMIN_PASSWORD`. Never commit `.env`.

For the recommended Docker database, keep the example connection string on
port 5433 and run:

```sh
make dev
```

This starts the pgvector database container, builds Noema, applies migrations,
and serves Noema at <http://127.0.0.1:8080>.

For native PostgreSQL, change `NOEMA_DATABASE_URL` in `.env` to use port 5432
and the password selected above, then build and migrate explicitly:

```sh
make build
set -a
. ./.env
set +a
./bin/noemactl migrate up
./bin/noemad
```

Avoid placing passwords containing URI-reserved characters directly in a
PostgreSQL URL unless they are percent-encoded.

## 5. Verify pgvector and Noema

For the Docker database:

```sh
docker compose -f deploy/docker-compose.yml exec -T db \
  psql -U noema -d noema \
  -c "SELECT extversion FROM pg_extension WHERE extname = 'vector';"

docker compose -f deploy/docker-compose.yml exec -T db \
  psql -U noema -d noema \
  -c "SELECT to_regclass('public.memory_embeddings');"
```

For native PostgreSQL, run the equivalent queries with `psql`:

```sh
psql "$NOEMA_DATABASE_URL" \
  -c "SELECT extversion FROM pg_extension WHERE extname = 'vector';"
psql "$NOEMA_DATABASE_URL" \
  -c "SELECT to_regclass('public.memory_embeddings');"
```

The first query should return a version and the second should return
`memory_embeddings`. If migration 0005 had already run before pgvector became
available, enabling the extension afterward does not rerun that migration; use
a new repair migration rather than editing an applied migration.

## 6. Optional development tools

Install the analyzers used by the Makefile:

```sh
make tools
```

Then run the standard checks:

```sh
make fmt lint test-race security
make test-db
```

`make test-db` uses the Docker database on port 5433 by default. To test with
native PostgreSQL instead, provide its connection string explicitly:

```sh
NOEMA_TEST_DATABASE_URL="$NOEMA_DATABASE_URL" go test -race -count=1 ./...
```
