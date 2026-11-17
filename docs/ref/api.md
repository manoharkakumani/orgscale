# API

Orgscale provides a [HTTP REST API](#rest-api) which may be used to integrate a [web
interface](integration/web-ui.md), [remote control Orgscale](#remote-control) or provide a base for custom
integration and tooling.

The API requires a valid API key before use. To create an API key, log into your Orgscale server and generate
one with the default expiration of 90 days:

```shell
orgscale apikeys create
```

Copy the output of the command and save it for later. Please note that you can not retrieve an API key again. If the API
key is lost, expire the old one, and create a new one.

To list the API keys currently associated with the server:

```shell
orgscale apikeys list
```

and to expire an API key:

```shell
orgscale apikeys expire --prefix <PREFIX>
```

## REST API

- API endpoint: `/api/v1`, e.g. `https://orgscale.example.com/api/v1`
- Documentation: `/api/v1/docs`, e.g. `https://orgscale.example.com/api/v1/docs`
- Orgscale Version: `/version`, e.g. `https://orgscale.example.com/version`
- Authenticate using HTTP Bearer authentication by sending the [API key](#api) with the HTTP `Authorization: Bearer <API_KEY>` header.

Start by [creating an API key](#api) and test it with the examples below. Read the API documentation provided by your
Orgscale server at `/api/v1/docs` for details.

=== "Get details for all users"

    ```console
    curl -H "Authorization: Bearer <API_KEY>" \
        https://orgscale.example.com/api/v1/user
    ```

=== "Get details for user 'bob'"

    ```console
    curl -H "Authorization: Bearer <API_KEY>" \
        https://orgscale.example.com/api/v1/user?name=bob
    ```

=== "Register a node"

    ```console
    curl -H "Authorization: Bearer <API_KEY>" \
        --json '{"user": "<USER>", "authId": "<AUTH_ID>"}' \
        https://orgscale.example.com/api/v1/auth/register
    ```

## Join nodes with an OAuth client

Orgscale also serves a subset of the Tailscale-compatible API at `/api/v2`, which
accepts **OAuth 2.0 client-credentials** in addition to API keys. The Tailscale
client can use an OAuth client secret in place of an auth key: it exchanges the
secret for an access token, mints a single-use tagged auth key and registers with
it. This works anywhere the client takes an auth key: `tailscale up`, the
container image, `tsnet` and the
[`tailscale/github-action`](https://github.com/tailscale/github-action). One
long-lived secret joins any number of nodes. A client keeps its newest 1000
access tokens and minting more ends the oldest, so give a long-lived integration
its own client.

Create an OAuth client with the `auth_keys` scope and the tags its nodes get. The
secret is shown once:

```shell
orgscale oauth-clients create --scope auth_keys --tag tag:ci
```

Scopes apply to the whole tailnet, as in Tailscale: an `auth_keys` token can read
and revoke auth keys of any tag, an `oauth_keys` token every OAuth client, and a
`devices:core:read` token reads every device, routes included with `fields=all`.
Keep the secret as safe as any credential that can revoke keys. Without
`all=true`, a token lists only the auth keys made through a token, whatever their
tags; with it, every valid auth key. No listing holds a spent, expired or revoked
key. Tags limit only the tags a token puts on keys, clients and devices, and
which tagged devices it may retag. Deleting an OAuth client revokes its secret
and its access tokens; auth keys, clients and nodes created with it stay until
you remove them.

Build the auth key from the secret:

1. Swap the prefix: `oskey-client-…` becomes `tskey-client-…`. The client only
   runs the exchange for `tskey-client-`; Orgscale accepts both.
1. Append `?baseURL=<your Orgscale URL>`.
1. Set `--advertise-tags`. Each tag must exist in the policy's `tagOwners` and be
   one of the OAuth client's tags, or owned by one of them.

```text
tskey-client-<id>-<secret>?baseURL=https://orgscale.example.com
```

!!! warning

    Without `baseURL` the client sends the secret to `https://api.tailscale.com`.

### Attributes

These are all the attributes the client understands; any other is an error.
Order does not matter and an empty value means the default.

| Attribute       | Default                     | Effect                                                                                           |
| --------------- | --------------------------- | ------------------------------------------------------------------------------------------------ |
| `baseURL`       | `https://api.tailscale.com` | Where the exchange and key creation go. Your Orgscale URL, no trailing slash.                   |
| `ephemeral`     | `true`                      | Node is removed after it goes offline (`node.ephemeral.inactivity_timeout`). `false` to keep it. |
| `preauthorized` | `false`                     | Accepted, no effect: Orgscale always authorizes pre-auth-key nodes.                             |

Booleans take any Go `strconv.ParseBool` value (`true`, `false`, `1`, `0`, …).

### Examples

`tailscale up`, either as the auth key or via `--client-secret` (which also takes
`file:/path/to/secret`):

```shell
tailscale up --login-server https://orgscale.example.com --advertise-tags tag:ci \
  --auth-key 'tskey-client-<id>-<secret>?baseURL=https://orgscale.example.com&ephemeral=false'
```

Container image:

```shell
docker run -d --name tailscale \
  -e TS_AUTHKEY='tskey-client-<id>-<secret>?baseURL=https://orgscale.example.com' \
  -e TS_EXTRA_ARGS='--login-server=https://orgscale.example.com --advertise-tags=tag:ci' \
  tailscale/tailscale
```

`tsnet` (`TS_CLIENT_SECRET` works too); import `tailscale.com/feature/oauthkey`:

```go
srv := &tsnet.Server{
    ControlURL:    "https://orgscale.example.com",
    AuthKey:       "tskey-client-<id>-<secret>?baseURL=https://orgscale.example.com",
    AdvertiseTags: []string{"tag:ci"},
}
```

GitHub Action, with the whole string stored as a repository secret:

{% raw %}

```yaml
- uses: tailscale/github-action@v4
  with:
    authkey: ${{ secrets.ORGSCALE_AUTHKEY }}
    args: --login-server=https://orgscale.example.com --advertise-tags=tag:ci
```

{% endraw %}

Use `authkey` even though upstream marks it deprecated: `oauth-secret` appends its
own `?…` to the secret, which corrupts `baseURL`.

## Remote control

The `orgscale` binary can control a Orgscale instance from a remote machine over the HTTP API.

### Prerequisite

- A workstation to run `orgscale` (any supported platform, e.g. Linux).
- The Orgscale server reachable over HTTP(S).
- An [API key](#api) to authenticate with the Orgscale server.

### Setup remote control

1. Download the [`orgscale` binary from GitHub's release page](https://github.com/juanfont/headscale/releases). Make
   sure to use the same version as on the server.

1. Put the binary somewhere in your `PATH`, e.g. `/usr/local/bin/orgscale`

1. Make `orgscale` executable: `chmod +x /usr/local/bin/orgscale`

1. [Create an API key](#api) on the Orgscale server.

1. Provide the connection parameters for the remote Orgscale server either via a minimal YAML configuration file or
   via environment variables:

    === "Minimal YAML configuration file"

        ```yaml title="config.yaml"
        cli:
            address: <ORGSCALE_URL>
            api_key: <API_KEY>
        ```

    === "Environment variables"

        ```shell
        export ORGSCALE_CLI_ADDRESS="<ORGSCALE_URL>"
        export ORGSCALE_CLI_API_KEY="<API_KEY>"
        ```

    This instructs the `orgscale` binary to connect to a remote instance at `<ORGSCALE_URL>` (e.g.
    `https://orgscale.example.com`), instead of connecting to the local instance. A bare host without a scheme is
    assumed to be `https`.

1. Test the connection by listing all nodes:

    ```shell
    orgscale nodes list
    ```

    You should now be able to see a list of your nodes from your workstation, and you can
    now control the Orgscale server from your workstation.

### Behind a proxy

The remote CLI uses the same HTTP API as everything else, so it works through the reverse proxy already in front of
Orgscale with no extra setup.

### Troubleshooting

- Make sure you have the _same_ Orgscale version on your server and workstation.
- Verify that your TLS certificate is valid and trusted.
- If you don't have access to a trusted certificate (e.g. from Let's Encrypt), either:
    - Add your self-signed certificate to the trust store of your OS _or_
    - Disable certificate verification by either setting `cli.insecure: true` in the configuration file or by setting
      `ORGSCALE_CLI_INSECURE=1` via an environment variable. We do **not** recommend to disable certificate validation.
