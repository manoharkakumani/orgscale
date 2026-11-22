# Running orgscale with Docker

The image contains only the `orgscale` binary. It holds no configuration and
no data, so the same image runs every installation:

| What | Where | Notes |
|---|---|---|
| Settings | `ORGSCALE_*` environment variables, or a file at `/etc/orgscale/config.yaml` | Either works; variables override the file. |
| Secrets | files, referenced as `file:/run/secrets/...` | Never put secrets in plain environment variables. |
| Data | volume at `/var/lib/orgscale` | SQLite database and the server's private key. Back it up. |

Every configuration key has an environment variable: `oidc.client_id` is
`ORGSCALE_OIDC_CLIENT_ID`, lists are JSON (`'["a@x.com","b@x.com"]'`). The
full list is in [configuration options](../ref/configuration-options.md).

## docker compose

```yaml
services:
  orgscale:
    image: ghcr.io/manoharkakumani/orgscale:latest   # or: build: .
    restart: unless-stopped
    ports:
      - "8080:8080"        # put HTTPS in front, see below
      - "3478:3478/udp"    # only with the embedded DERP server
    volumes:
      - orgscale-data:/var/lib/orgscale
    secrets:
      - oidc_client_secret
    environment:
      ORGSCALE_SERVER_URL: https://vpn.example.com
      ORGSCALE_LISTEN_ADDR: 0.0.0.0:8080
      ORGSCALE_POLICY_MODE: database
      ORGSCALE_OIDC_ISSUER: https://accounts.google.com
      ORGSCALE_OIDC_CLIENT_ID: 1234567890-abc.apps.googleusercontent.com
      ORGSCALE_OIDC_CLIENT_SECRET: file:/run/secrets/oidc_client_secret
      ORGSCALE_OIDC_SCOPE: '["openid","profile","email"]'
      ORGSCALE_ADMIN_ENABLED: "true"
      ORGSCALE_ADMIN_USERS: '["you@example.com"]'

volumes:
  orgscale-data:

secrets:
  oidc_client_secret:
    file: ./oidc_client_secret.txt
```

```bash
docker compose up -d
docker compose exec orgscale orgscale users list   # the CLI works inside
```

Then open `https://vpn.example.com/admin/` and sign in with SSO. Everything
else (who can sign in, admins, SCIM, Google sync, servers, access) is set up
in the console.

## Without any SSO settings

The `ORGSCALE_OIDC_*` and `ORGSCALE_ADMIN_USERS` lines are optional. Leave
them out and orgscale starts in setup mode: `docker compose logs orgscale`
shows a one-time setup link where single sign-on is configured in the
browser. If it expired:

```bash
docker compose exec orgscale orgscale admin setup-link
```

## HTTPS

Run orgscale behind HTTPS. Either let orgscale get a Let's Encrypt
certificate itself (`ORGSCALE_TLS_ACME_HOSTNAME=vpn.example.com`, `ORGSCALE_LISTEN_ADDR=0.0.0.0:443`,
publish port 443), or put a TLS proxy such as Caddy or Traefik in front and
forward to port 8080. Sign-in cookies, keys and SCIM tokens must never travel
over plain HTTP.

## Several tailnets on one machine

Each tailnet is its own orgscale container, with its own address, volume
and database. One Caddy serves them all:

```yaml
services:
  prod:
    image: ghcr.io/manoharkakumani/orgscale:latest
    restart: unless-stopped
    volumes: [prod-data:/var/lib/orgscale]
    environment:
      ORGSCALE_SERVER_URL: https://vpn.example.com
      ORGSCALE_LISTEN_ADDR: 0.0.0.0:8080
      ORGSCALE_POLICY_MODE: database
      ORGSCALE_PREFIXES_V4: 100.64.0.0/11

  staging:
    image: ghcr.io/manoharkakumani/orgscale:latest
    restart: unless-stopped
    volumes: [staging-data:/var/lib/orgscale]
    environment:
      ORGSCALE_SERVER_URL: https://vpn-staging.example.com
      ORGSCALE_LISTEN_ADDR: 0.0.0.0:8080
      ORGSCALE_POLICY_MODE: database
      ORGSCALE_PREFIXES_V4: 100.96.0.0/11

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy-data:/data

volumes:
  prod-data:
  staging-data:
  caddy-data:
```

```
vpn.example.com {
    reverse_proxy prod:8080
}

vpn-staging.example.com {
    reverse_proxy staging:8080
}
```

Each one prints its own setup link (`docker compose logs staging`). Giving
them different address ranges, as above, lets one person's laptop join both
(with `tailscale switch`) without the addresses clashing. Link the consoles to
each other under **Settings → Tailnets**.

## Building the image yourself

```bash
docker build -t orgscale --build-arg VERSION=$(git describe --tags --always) .
```

The image is about 20 MB, runs as a non-root user and has no shell.
