# orgscale

orgscale is a self-hosted control server for Tailscale clients, built for
running one organization's network without hand-managing users:

- a **web admin console** at `/admin/` with single sign-on for administrators;
- **SSO-driven users**: people join by signing in with your identity provider,
  and leave automatically through **SCIM** (Okta, Entra ID, OneLogin,
  Authentik) or **Google Workspace** sync;
- **device access control**: with restriction on, each person only sees their
  own devices and the devices assigned to them, directly or through an IdP
  group;
- **roles** (admin, read-only viewer) and an **audit log**.

Set-up guide: [docs/orgscale/admin-console.md](docs/orgscale/admin-console.md).

orgscale is a fork of [Headscale](https://github.com/juanfont/headscale)
(BSD-3-Clause). The rest of this README and the reference documentation under
`docs/` describe the Headscale core it builds on; read `headscale` there as
`orgscale`. orgscale is not affiliated with Tailscale Inc. or the Headscale
project.

## What is Tailscale

Tailscale is [a modern VPN](https://tailscale.com/) built on top of
[Wireguard](https://www.wireguard.com/).
It [works like an overlay network](https://tailscale.com/blog/how-tailscale-works/)
between the computers of your networks - using
[NAT traversal](https://tailscale.com/blog/how-nat-traversal-works/).

Everything in Tailscale is Open Source, except the GUI clients for proprietary OS
(Windows and macOS/iOS), and the control server.

The control server works as an exchange point of Wireguard public keys for the
nodes in the Tailscale network. It assigns the IP addresses of the clients,
creates the boundaries between each user, enables sharing machines between users,
and exposes the advertised routes of your nodes.

A [Tailscale network (tailnet)](https://tailscale.com/docs/concepts/tailnet) is
private network which Tailscale assigns to a user in terms of private users or an
organisation.

## Design goal

Orgscale aims to implement a self-hosted, open source alternative to the
[Tailscale](https://tailscale.com/) control server. Orgscale's goal is to
provide self-hosters and hobbyists with an open-source server they can use for
their projects and labs. It implements a narrow scope, a _single_ Tailscale
network (tailnet), suitable for a personal use, or a small open-source
organisation.

## Supporting Orgscale

If you like `orgscale` and find it useful, there is a sponsorship and donation
buttons available in the repo.

## Features

Everything Headscale does, plus the admin console, single sign-on driven user
management, directory sync (SCIM, Google Workspace), server tags, visual
access rules and an audit log. See the
[admin console guide](docs/orgscale/admin-console.md).

## Client OS support

orgscale works with the official Tailscale clients on macOS, Windows, Linux,
iOS and Android. See [connecting devices](docs/orgscale/admin-console.md#connecting-devices).

## Running orgscale

Run it with Docker ([guide](docs/orgscale/docker.md)), then follow the
[admin console guide](docs/orgscale/admin-console.md). Server
settings not covered there work as in Headscale; the
[configuration reference](docs/ref/configuration-options.md) lists them all.

### First-time setup

A new installation has no identity provider yet, so nobody can sign in. There
is no built-in admin password. Instead, orgscale writes a one-time setup link
to its log when it starts:

```
  orgscale is not set up yet. To finish setup, open

      https://vpn.example.com/admin/setup#oskey-setup-…
```

Open it, connect your identity provider (Google Workspace, Microsoft Entra ID,
Okta or any OpenID Connect provider) and enter your email as the first
administrator. Once you sign in, setup closes and the link stops working.

The link works once and expires after 30 minutes. To get a new one, run on
the server:

```bash
orgscale admin setup-link

# with Docker
docker compose exec orgscale orgscale admin setup-link
```

Each run replaces the previous link. The command only works on the server
itself (over its local socket), so nobody can make a link from outside. Use
the same command to get back in if nobody can sign in any more, for example
after your identity provider changed. Restarting orgscale without single
sign-on configured also prints a fresh link.

For NixOS users, a module is available in [`nix/`](./nix/).

## Builds from `main`

Build from source with `make build`; the console is embedded, so Node.js is
only needed to change it (`make admin-ui`).

## Talks

- Fosdem 2026 (video): [Orgscale & Tailscale: The complementary open source clone](https://fosdem.org/2026/schedule/event/KYQ3LL-orgscale-the-complementary-open-source-clone/)
  - presented by Kristoffer Dalby
- Fosdem 2023 (video): [Orgscale: How we are using integration testing to reimplement Tailscale](https://fosdem.org/2023/schedule/event/goorgscale/)
  - presented by Juan Font Alonso and Kristoffer Dalby

## Disclaimer

This project is not associated with Tailscale Inc.

However, one of the active maintainers for Orgscale [is employed by Tailscale](https://tailscale.com/blog/opensource) and he is allowed to spend work hours contributing to the project. Contributions from this maintainer are reviewed by other maintainers.

The maintainers work together on setting the direction for the project. The underlying principle is to serve the community of self-hosters, enthusiasts and hobbyists - while having a sustainable project.

## Contributing

Please read the [CONTRIBUTING.md](./CONTRIBUTING.md) file.

Have also a look at our [AI_POLICY.md](./AI_POLICY.md).

### Requirements

To contribute to orgscale you would need the latest version of [Go](https://golang.org)
and [Buf](https://buf.build) (Protobuf generator).

We recommend using [Nix](https://nixos.org/) to setup a development environment. This can
be done with `nix develop`, which will install the tools and give you a shell.
This guarantees that you will have the same dev env as `orgscale` maintainers.

### Code style

To ensure we have some consistency with a growing number of contributions,
this project has adopted linting and style/formatting rules:

The **Go** code is linted with [`golangci-lint`](https://golangci-lint.run) and
formatted with the `golangci-lint` formatters
([`gofumpt`](https://github.com/mvdan/gofumpt), `gci`, `goimports`).
Please configure your editor to run the tools while developing and make sure to
run `make lint` and `make fmt` before committing any code.

The **Proto** code is linted with [`buf`](https://docs.buf.build/lint/overview) and
formatted with [`clang-format`](https://clang.llvm.org/docs/ClangFormat.html).

The **docs** are formatted with [`mdformat`](https://mdformat.readthedocs.io).

The **rest** (Markdown, YAML, etc) is formatted with [`prettier`](https://prettier.io).

Check out the `.golangci.yaml` and `Makefile` to see the specific configuration.

### Install development tools

- Go
- Buf
- Protobuf tools

Install and activate:

```shell
nix develop
```

### Testing and building

Some parts of the project require the generation of Go code from Protobuf
(if changes are made in `proto/`) and it must be (re-)generated with:

```shell
make generate
```

**Note**: Please check in changes from `gen/` in a separate commit to make it easier to review.

To run the tests:

```shell
make test
```

To build the program:

```shell
make build
```

### Development workflow

We recommend using Nix for dependency management to ensure you have all required tools. If you prefer to manage dependencies yourself, you can use Make directly:

**With Nix (recommended):**

```shell
nix develop
make test
make build
```

**With your own dependencies:**

```shell
make test
make build
```

The Makefile will warn you if any required tools are missing and suggest running `nix develop`. Run `make help` to see all available targets.

## Contributors

<a href="https://github.com/juanfont/headscale/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=juanfont/headscale" />
</a>

Made with [contrib.rocks](https://contrib.rocks).
