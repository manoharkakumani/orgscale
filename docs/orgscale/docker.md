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

## HTTPS

Run orgscale behind HTTPS. Either let orgscale get a Let's Encrypt
certificate itself (`ORGSCALE_TLS_LETSENCRYPT_HOSTNAME=vpn.example.com`,
publish port 443), or put a TLS proxy such as Caddy or Traefik in front and
forward to port 8080. Sign-in cookies, keys and SCIM tokens must never travel
over plain HTTP.

## Building the image yourself

```bash
docker build -t orgscale --build-arg VERSION=$(git describe --tags --always) .
```

The image is about 20 MB, runs as a non-root user and has no shell.
