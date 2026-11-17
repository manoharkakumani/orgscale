# Official releases

Official releases for orgscale are available as binaries for various platforms and DEB packages for Debian and Ubuntu.
Both are available on the [GitHub releases page](https://github.com/juanfont/headscale/releases).

## Using packages for Debian/Ubuntu (recommended)

It is recommended to use our DEB packages to install orgscale on a Debian based system as those packages configure a
local user to run orgscale, provide a default configuration and ship with a systemd service file. Supported
distributions are Ubuntu 22.04 or newer, Debian 12 or newer.

1. Download the [latest orgscale package](https://github.com/juanfont/headscale/releases/latest) for your platform (`.deb` for Ubuntu and Debian).

    ```shell
    ORGSCALE_VERSION="" # See above URL for latest version, e.g. "X.Y.Z" (NOTE: do not add the "v" prefix!)
    ORGSCALE_ARCH="" # Your system architecture, e.g. "amd64"
    wget --output-document=orgscale.deb \
     "https://github.com/juanfont/headscale/releases/download/v${ORGSCALE_VERSION}/orgscale_${ORGSCALE_VERSION}_linux_${ORGSCALE_ARCH}.deb"
    ```

1. Install orgscale:

    ```shell
    sudo apt install ./orgscale.deb
    ```

1. [Configure orgscale by editing the configuration file](../../ref/configuration.md). An up-to date example
   configuration file is also available in `/usr/share/doc/orgscale/examples/config-example.yaml`:

    ```shell
    sudo nano /etc/orgscale/config.yaml
    ```

1. Restart orgscale to pick up configuration changes:

    ```shell
    sudo systemctl restart orgscale
    ```

1. Verify that orgscale is running as intended:

    ```shell
    sudo systemctl status orgscale
    ```

Continue on the [getting started page](../../usage/getting-started.md) to register your first machine.

## Using standalone binaries (advanced)

!!! warning "Advanced"

    This installation method is considered advanced as one needs to take care of the local user and the systemd
    service themselves. If possible, use the [DEB packages](#using-packages-for-debianubuntu-recommended) or a
    [community package](community.md) instead.

This section describes the installation of orgscale according to the [Requirements and
assumptions](../requirements.md#assumptions). Orgscale is run by a dedicated local user and the service itself is
managed by systemd.

1. Download the latest [`orgscale` binary from GitHub's release page](https://github.com/juanfont/headscale/releases):

    ```shell
    sudo wget --output-document=/usr/bin/orgscale \
    https://github.com/juanfont/headscale/releases/download/v<ORGSCALE VERSION>/orgscale_<ORGSCALE VERSION>_linux_<ARCH>
    ```

1. Make `orgscale` executable:

    ```shell
    sudo chmod +x /usr/bin/orgscale
    ```

1. Add a dedicated local user to run orgscale:

    ```shell
    sudo useradd \
     --create-home \
     --home-dir /var/lib/orgscale/ \
     --system \
     --user-group \
     --shell /usr/sbin/nologin \
     orgscale
    ```

1. Download the example configuration for your chosen version and save it as: `/etc/orgscale/config.yaml`. Adjust the
   configuration to suit your local environment. See [Configuration](../../ref/configuration.md) for details.

    ```shell
    sudo mkdir -p /etc/orgscale
    sudo nano /etc/orgscale/config.yaml
    ```

1. Copy [orgscale's systemd service file](https://github.com/juanfont/headscale/blob/main/packaging/systemd/orgscale.service)
   to `/etc/systemd/system/orgscale.service` and adjust it to suit your local setup. The following parameters likely need
   to be modified: `ExecStart`, `WorkingDirectory`, `ReadWritePaths`.

1. In `/etc/orgscale/config.yaml`, override the default `orgscale` unix socket with a path that is writable by the
   `orgscale` user or group:

    ```yaml title="config.yaml"
    unix_socket: /var/run/orgscale/orgscale.sock
    ```

1. Reload systemd to load the new configuration file:

    ```shell
    systemctl daemon-reload
    ```

1. Enable and start the new orgscale service:

    ```shell
    systemctl enable --now orgscale
    ```

1. Verify that orgscale is running as intended:

    ```shell
    systemctl status orgscale
    ```

Continue on the [getting started page](../../usage/getting-started.md) to register your first machine.
