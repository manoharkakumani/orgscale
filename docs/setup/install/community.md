# Community packages

Several Linux distributions and community members provide packages for orgscale. Those packages may be used instead of
the [official releases](official.md) provided by the orgscale maintainers. Such packages offer improved integration
for their targeted operating system and usually:

- setup a dedicated local user account to run orgscale
- provide a default configuration
- install orgscale as system service

!!! warning "Community packages might be outdated"

    The packages mentioned on this page might be outdated or unmaintained. Use the [official releases](official.md) to
    get the current stable version or to [test pre-releases](main.md).

    [![Packaging status](https://repology.org/badge/vertical-allrepos/orgscale.svg)](https://repology.org/project/orgscale/versions)

## Arch Linux

Arch Linux offers a package for orgscale, install via:

```shell
pacman -S orgscale
```

## Fedora, RHEL, CentOS

A third-party repository for various RPM based distributions is available at:
<https://copr.fedorainfracloud.org/coprs/jonathanspw/orgscale/>. The site provides detailed setup and installation
instructions.

## Nix, NixOS

A Nix package is available as: `orgscale`. See the [NixOS package site for installation
details](https://search.nixos.org/packages?show=orgscale).

## Gentoo

```shell
emerge --ask net-vpn/orgscale
```

Gentoo specific documentation is available [here](https://wiki.gentoo.org/wiki/User:Maffblaster/Drafts/Orgscale).

## OpenBSD

Orgscale is available in ports. The port installs orgscale as system service with `rc.d` and provides usage
instructions upon installation.

```shell
pkg_add orgscale
```
