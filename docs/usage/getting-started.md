# Getting started

This page helps you get started with orgscale and provides a few usage examples for the orgscale command line tool
`orgscale`.

!!! note "Prerequisites"

    - Orgscale is installed and running as system service. Read the [setup section](../setup/requirements.md) for
      installation instructions.
    - The configuration file exists and is adjusted to suit your environment, see
      [Configuration](../ref/configuration.md) for details.
    - Orgscale is reachable from the Internet. Verify this by visiting the health endpoint:
      https://orgscale.example.com/health
    - The Tailscale client is installed, see [Client and operating system support](../about/clients.md) for more
      information.

## Getting help

The `orgscale` command line tool provides built-in help. To show available commands along with their arguments and
options, run:

=== "Native"

    ```shell
    # Show help
    orgscale help

    # Show help for a specific command
    orgscale <COMMAND> --help
    ```

=== "Container"

    ```shell
    # Show help
    docker exec -it orgscale \
      orgscale help

    # Show help for a specific command
    docker exec -it orgscale \
      orgscale <COMMAND> --help
    ```

!!! note "Manage orgscale from another local user"

    By default only the user `orgscale` or `root` will have the necessary permissions to access the unix socket
    (`/var/run/orgscale/orgscale.sock`) that is used to communicate with the service. In order to be able to
    communicate with the orgscale service you have to make sure the unix socket is accessible by the user that runs
    the commands. In general you can achieve this by any of the following methods:

    - using `sudo`
    - run the commands as user `orgscale`
    - add your user to the `orgscale` group

    To verify you can run the following command using your preferred method:

    ```shell
    orgscale users list
    ```

## Manage orgscale users

In orgscale, a node (also known as machine or device) is [typically assigned to a orgscale
user](../ref/registration.md#identity-model). Such a orgscale user[^1] may have many nodes assigned to them and can be
managed with the `orgscale users` command. Invoke the built-in help for more information: `orgscale users --help`.

### Create a orgscale user

=== "Native"

    ```shell
    orgscale users create <USER>
    ```

=== "Container"

    ```shell
    docker exec -it orgscale \
      orgscale users create <USER>
    ```

### List existing orgscale users

=== "Native"

    ```shell
    orgscale users list
    ```

=== "Container"

    ```shell
    docker exec -it orgscale \
      orgscale users list
    ```

## Register a node

One has to [register a node](../ref/registration.md) first to use orgscale as coordination server with Tailscale. The
following examples work for the Tailscale client on Linux/BSD operating systems. Alternatively, follow the instructions
to connect [Android](connect/android.md), [Apple](connect/apple.md) or [Windows](connect/windows.md) devices. Read
[registration methods](../ref/registration.md) for an overview of available registration methods.

### [Web authentication](../ref/registration.md#web-authentication)

On a client machine, run the `tailscale up` command and provide the FQDN of your orgscale instance as argument:

```shell
tailscale up --login-server <YOUR_ORGSCALE_URL>
```

Usually, a browser window with further instructions is opened. This page explains how to complete the registration on
your orgscale server and it also prints the Auth ID required to approve the node:

=== "Native"

    ```shell
    orgscale auth register --user <USER> --auth-id <AUTH_ID>
    ```

=== "Container"

    ```shell
    docker exec -it orgscale \
      orgscale auth register --user <USER> --auth-id <AUTH_ID>
    ```

### [Pre authenticated key](../ref/registration.md#pre-authenticated-key)

It is also possible to generate a preauthkey and register a node non-interactively. First, generate a preauthkey on the
orgscale instance. By default, the key is valid for one hour and can only be used once (see `orgscale preauthkeys --help` for other options):

=== "Native"

    ```shell
    orgscale preauthkeys create --user <USER>
    ```

=== "Container"

    ```shell
    docker exec -it orgscale \
      orgscale preauthkeys create --user <USER>
    ```

The command returns the preauthkey on success which is used to connect a node to the orgscale instance via the
`tailscale up` command:

```shell
tailscale up --login-server <YOUR_ORGSCALE_URL> --authkey <YOUR_AUTH_KEY>
```

[^1]: [Ensure that the Orgscale username does not end with `@`.](../ref/oidc.md#reference-a-user-in-the-policy)
