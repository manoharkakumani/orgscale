# Orgscale

Orgscale is a self-hosted control server for the official
[Tailscale](https://tailscale.com) apps, built for running **one
organization's private network** with no hand-managed users, keys or policy
files.

People sign in with your identity provider, their devices join, and they
reach only what they have been given. When someone leaves, your directory
removes them, and their devices go with them. Everything is managed from a
web console; nobody needs to write policy files.

Orgscale is a fork of [Headscale](https://github.com/juanfont/headscale).
See [Credits](#credits).

## Features

**Identity**

- Single sign-on with Microsoft Entra ID, Google Workspace, Okta or any
  OpenID Connect provider, set up in the browser on first start
- Automatic joiners and leavers: Microsoft Entra ID sync (Microsoft Graph),
  Google Workspace sync, or SCIM (Okta, Entra ID, OneLogin, JumpCloud,
  Authentik)
- Console roles (admin, read-only), every change in an audit log

**Access**

- Access control that is off by default and one switch to turn on: people
  reach their own devices and what they have been given, nothing else
- Servers grouped by tag (`tag:databases`), with who can reach each tag
- Access rules built in the console: people, groups, tags or single
  devices, by port and protocol
- SSH without keys (Tailscale SSH), limited to chosen groups and Linux
  accounts, with optional re-authentication through single sign-on
- Temporary access: give people or groups a tag or one server for 15
  minutes to 7 days; it ends on its own

**Devices and network**

- One link to send people (`/connect`), with downloads for Mac and Windows
  and settings for Intune, Jamf and Kandji
- Servers join with a one-line command from the console
- DNS from the console: MagicDNS names, DNS servers, split DNS for internal
  domains, custom records; changes reach devices without a restart
- Subnet routers and exit nodes for reaching private networks (AWS VPCs,
  Azure VNets and so on)

**The console**

- Light and dark themes, works on phones
- Secure by default: HTTPS only, server-side sessions, CSRF protection, rate
  limits, no built-in admin password

## Setup

You need a Linux machine with Docker and a public IP, and a DNS name
pointing at it, for example `vpn.example.com`. One small VM (1 vCPU, 1 to 2
GB) is enough for an organization.

Pick that name before connecting anyone: devices remember the address they
joined with.

### 1. Run it

The image is published to GitHub Container Registry as
`ghcr.io/manoharkakumani/orgscale`, for amd64 and arm64. If the repository is
private, sign in first with a token that has `read:packages`:

```bash
echo <token> | docker login ghcr.io -u <github-user> --password-stdin
```

`docker-compose.yml`, with [Caddy](https://caddyserver.com) providing HTTPS
through Let's Encrypt:

```yaml
services:
  orgscale:
    image: ghcr.io/manoharkakumani/orgscale:latest
    restart: unless-stopped
    volumes:
      - orgscale-data:/var/lib/orgscale
    environment:
      ORGSCALE_SERVER_URL: https://vpn.example.com
      ORGSCALE_LISTEN_ADDR: 0.0.0.0:8080
      ORGSCALE_POLICY_MODE: database

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy-data:/data

volumes:
  orgscale-data:
  caddy-data:
```

`Caddyfile`:

```
vpn.example.com {
    reverse_proxy orgscale:8080
}
```

```bash
docker compose up -d
```

Open ports 80 and 443. More options (settings files, secrets, PostgreSQL,
building the image) are in the [Docker guide](docs/orgscale/docker.md).

### 2. First-time setup

A new installation has no identity provider and no admin password, so it
writes a one-time setup link to its log:

```bash
docker compose logs orgscale | grep -A2 "not set up"
```

```
  orgscale is not set up yet. To finish setup, open

      https://vpn.example.com/admin/setup#oskey-setup-…
```

Open it, pick your identity provider, follow the steps shown, and enter your
own email as the first administrator. Once you sign in, setup closes and the
link stops working.

The link works once and expires after 30 minutes. For a new one:

```bash
docker compose exec orgscale orgscale admin setup-link
```

It only works on the server itself, so nobody can make a link from outside.
Use it too if nobody can sign in any more.

**Redirect URLs to register with your identity provider:**

- `https://vpn.example.com/oidc/callback` (devices)
- `https://vpn.example.com/admin/auth/callback` (the console)

### 3. Bring in your people

In **Settings**:

- **Who can sign in**: your email domains (for example `example.com`).
- **Microsoft Entra ID**, **Google Workspace** or **SCIM**: turn on directory
  sync so users and groups follow your directory, and leavers are removed.
  Entra sync needs the Microsoft Graph *application* permissions
  `User.Read.All`, `Group.Read.All`, `GroupMember.Read.All` and
  `Organization.Read.All`, with admin consent.
- **Console access**: more administrators or read-only viewers. Nobody else
  can sign in to the console.

### 4. Add servers and access

1. **Servers → New tag**, e.g. `tag:databases`, then **Add server**: copy
   the one-line command onto the server.
2. **Servers → Edit access**: who may reach the tag. Optionally the Linux
   accounts people may SSH in as without keys.
3. **Access**: turn access control on, and add rules for anything else.
4. **Temporary access**: give someone a tag or one server for a while.

### 5. Connect people's devices

Send everyone `https://vpn.example.com/connect`. It has the steps for each
platform and downloads the settings, so nobody types a command. Signing in
opens your identity provider; the device belongs to whoever signs in.

Company devices need no steps from people: push the server address with
Intune, Jamf or Kandji (macOS profile, Windows registry value, iOS
`ControlURL`, Android `LoginURL`). The console's **Overview** has the
downloads and values.

The [admin console guide](docs/orgscale/admin-console.md) covers all of this
in detail.

## Updating

```bash
docker compose pull && docker compose up -d
```

Database changes are applied on start. Back up the `orgscale-data` volume
(database and the server's private key) regularly; losing the key means every
device must join again.

## Building from source

Orgscale is written in Go; the console is React and TypeScript, built into
the binary.

```bash
make build                      # the orgscale binary, with the console embedded
make test                       # tests
cd web/admin && npm ci && npm run dev   # console development server
docker build -t orgscale .      # container image
```

Go 1.27 or newer. Node.js is only needed to change the console. `nix develop`
gives the exact toolchain CI uses. See [CONTRIBUTING.md](CONTRIBUTING.md) and
[AGENTS.md](AGENTS.md) for conventions.

## Documentation

- [Admin console guide](docs/orgscale/admin-console.md): setup, access,
  servers, SSH, temporary access, DNS, directory sync, security
- [Docker guide](docs/orgscale/docker.md)
- [Configuration reference](docs/ref/configuration-options.md): every setting,
  as `config.yaml` keys and `ORGSCALE_*` variables
- Inherited Headscale references under [`docs/`](docs/): [DNS](docs/ref/dns.md),
  [routes](docs/ref/routes.md), [DERP relays](docs/ref/derp.md),
  [policy](docs/ref/policy.md), [TLS](docs/ref/tls.md)

## Credits

Orgscale is built on [Headscale](https://github.com/juanfont/headscale), and
all credit for the control server goes to the Headscale project and
[its contributors](https://github.com/juanfont/headscale/graphs/contributors):
node registration, the policy engine, the network map, DERP, the CLI and the
API are their work. Orgscale adds the admin console, single sign-on setup,
directory sync, access management, temporary access, the device connection
pages and container packaging. Headscale's history is kept in this
repository, and its license and copyright notice in [LICENSE](LICENSE).

Questions or problems about Orgscale belong in this repository's issues, not
Headscale's.

## License

BSD 3-Clause, as Headscale. See [LICENSE](LICENSE). The original copyright
notice is kept as the license requires; Orgscale's changes are released
under the same terms.

Orgscale is not affiliated with or endorsed by Tailscale Inc. "Tailscale" is
a trademark of Tailscale Inc.; "WireGuard" is a registered trademark of
Jason A. Donenfeld.
