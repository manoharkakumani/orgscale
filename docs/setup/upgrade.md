# Upgrade an existing installation

!!! tip "Required update path"

    It's required to update from one stable version to the next (e.g. 0.26.0 → 0.27.1 → 0.28.0) without skipping minor
    versions in between. You should always pick the latest available patch release.

Update an existing Orgscale installation to a new version:

- Read the announcement on the [GitHub releases](https://github.com/juanfont/headscale/releases) page for the new
  version. It lists the changes of the release along with possible breaking changes and version-specific upgrade
  instructions.
- Stop Orgscale
- **[Create a backup of your installation](#backup)**
- Update Orgscale to the new version, preferably by following the same installation method.
- Compare and update the [configuration](../ref/configuration.md) file.
- Start Orgscale

## Backup

Orgscale applies database migrations during upgrades and we highly recommend to create a backup of your database before
upgrading. A full backup of Orgscale depends on your individual setup, but below are some typical setup scenarios.

=== "Standard installation"

    An installation that follows our [official releases](install/official.md) setup guide uses the following paths:

    - [Configuration file](../ref/configuration.md): `/etc/orgscale/config.yaml`
    - Data directory: `/var/lib/orgscale`
    - SQLite as database: `/var/lib/orgscale/db.sqlite`

    ```console
    TIMESTAMP=$(date +%Y%m%d%H%M%S)
    cp -aR /etc/orgscale /etc/orgscale.backup-$TIMESTAMP
    cp -aR /var/lib/orgscale /var/lib/orgscale.backup-$TIMESTAMP
    ```

=== "Container"

    An installation that follows our [container](install/container.md) setup guide uses a single source volume directory
    that contains the configuration file, data directory and the SQLite database.

    ```console
    cp -aR /path/to/orgscale /path/to/orgscale.backup-$(date +%Y%m%d%H%M%S)
    ```

=== "PostgreSQL"

    Please follow PostgreSQL's [Backup and Restore](https://www.postgresql.org/docs/current/backup.html) documentation
    to create a backup of your PostgreSQL database.
