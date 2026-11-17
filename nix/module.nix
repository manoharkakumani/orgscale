{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.orgscale;

  dataDir = "/var/lib/orgscale";
  runDir = "/run/orgscale";

  settingsFormat = pkgs.formats.yaml { };

  # orgscale reads a non-empty variable of the unit over the setting.
  effective =
    name: setting:
    let
      v = config.systemd.services.orgscale.environment.${name} or null;
    in
    if v == null || v == "" then setting else v;
  policyMode = effective "ORGSCALE_POLICY_MODE" cfg.settings.policy.mode;
  policyPath = effective "ORGSCALE_POLICY_PATH" cfg.settings.policy.path;
  # Interpolated as the config and the unit are, so a Nix path is the same store path.
  policyFile = if policyMode == "file" && policyPath != "" then "${policyPath}" else null;
  policyFileCheck = cfg.check.policy && policyFile != null;
  # Build-time checks run the host's orgscale.
  canExecute = pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform;
  # The build can read a store path.
  buildTimePolicy = policyFileCheck && lib.hasPrefix "${builtins.storeDir}/" policyFile && canExecute;
  preSwitchPolicy = policyFileCheck && !buildTimePolicy;

  # Interpolated as nixpkgs renders Environment=, so a Nix path is the same store path.
  unitEnv = lib.mapAttrsToList (n: v: "${n}=${v}") (
    lib.filterAttrs (_: v: v != null) config.systemd.services.orgscale.environment
  );

  policyCheck = pkgs.runCommand "orgscale-policy-check" { } ''
    ${lib.getExe cfg.package} check policy -f ${lib.escapeShellArg policyFile}
    touch $out
  '';

  # check config, not check all: the files the config names are not in the sandbox.
  configCheck = pkgs.runCommand "orgscale-config-check" { } ''
    env -i ${lib.escapeShellArgs unitEnv} ${lib.getExe cfg.package} check config -f ${cfg.configFile} || {
      echo "orgscale: this check sees the unit's environment, not its EnvironmentFile or %-specifiers expanded; services.orgscale.check.config = false skips it" >&2
      exit 1
    }
    touch $out
  '';

  # Its own script, for shellcheck and runtimeInputs.
  preSwitchCheck =
    let
      # systemd gives the unit Group= and the account's /etc/group memberships.
      # Activation rewrites the account's memberships in the groups NixOS
      # declares (members holds extraGroups), so those come from the new
      # configuration. With mutableUsers it keeps a hand-made group.
      groups = [
        "orgscale"
      ]
      ++ map (g: g.name) (
        lib.filter (g: lib.elem "orgscale" g.members) (lib.attrValues config.users.groups)
      )
      ++ lib.filter (g: g != "") (
        lib.concatMap (g: lib.splitString " " (toString g)) (
          lib.toList (config.systemd.services.orgscale.serviceConfig.SupplementaryGroups or [ ])
        )
      );
      declared = map (g: g.name) (lib.attrValues config.users.groups);
    in
    pkgs.writeShellApplication {
      name = "orgscale-pre-switch-check";
      runtimeInputs = [
        pkgs.coreutils
        pkgs.getent
        pkgs.util-linux
      ];
      text = ''
        # An unchanged unit is not restarted, so there is nothing to protect.
        unit=etc/systemd/system/orgscale.service
        if [ "$(readlink -f "/run/current-system/$unit")" = "$(readlink -f "$1/$unit")" ]; then
          echo "orgscale: unit unchanged, skipping checks"
          exit 0
        fi
        # Activation creates users and groups after this check runs.
        if ! id -u orgscale >/dev/null 2>&1; then
          echo "orgscale: user orgscale does not exist yet, skipping checks"
          exit 0
        fi
        if ! getent group orgscale >/dev/null; then
          echo "orgscale: group orgscale does not exist yet, skipping checks"
          exit 0
        fi
        trap '[ "$?" = 0 ] || echo "orgscale: to skip, set services.orgscale.check.config and check.policy to false, or run once with NIXOS_NO_CHECK=1"' EXIT

        # A group this switch creates does not exist yet; setpriv would fail on it.
        want=(${lib.escapeShellArgs groups})
        ${lib.optionalString config.users.mutableUsers ''
          # Not the account's primary group, which the unit does not get, nor a
          # group NixOS declares now, or declared before and activation removes.
          skip=" $(id -gn orgscale 2>/dev/null || true) "${lib.escapeShellArg (toString declared)}" $(cat /var/lib/nixos/declarative-groups 2>/dev/null || true) "
          for g in $(id -Gn orgscale); do
            [[ $skip == *" $g "* ]] || want+=("$g")
          done
        ''}
        groups=()
        for g in "''${want[@]}"; do
          if getent group "$g" >/dev/null; then groups+=("$g"); fi
        done
        grouplist=$(IFS=,; echo "''${groups[*]}")

        # As the unit's user and groups, with the unit's environment rather than the caller's.
        hs() {
          setpriv --reuid=orgscale --regid=orgscale --groups="$grouplist" -- \
            env -i ${lib.escapeShellArgs unitEnv} ${lib.getExe cfg.package} "$@"
        }

        ${lib.optionalString cfg.check.config ''
          echo "orgscale: checking the new configuration"
          hs check all -f ${cfg.configFile}
        ''}
        ${lib.optionalString preSwitchPolicy ''
          echo "orgscale: checking the new policy"
          hs check policy -f ${lib.escapeShellArg policyFile}
        ''}
      '';
    };

  # The option's type admits a number or http/https as the port, or an empty
  # listen_addr, which grants nothing. Past four digits a port is never
  # privileged, and the cap keeps toIntBase10 from throwing on a number too
  # long for an integer.
  listenPort = lib.last (lib.splitString ":" cfg.settings.listen_addr);
  listenDigits = builtins.match "0*([0-9]{1,4})" listenPort;
  privilegedPort =
    lib.elem listenPort [
      "http"
      "https"
    ]
    || (listenDigits != null && lib.toIntBase10 (lib.head listenDigits) < 1024);

  # Below lib.mkDefault, so a user's own mkDefault replaces these values
  # instead of conflicting with them.
  distroDefault = lib.mkOverride 1200;

  # Go reads cli, nested or dotted, as the client-only cli.*; written to the
  # config, cli.api_key would land in the world-readable store.
  cliKeys = lib.filter (n: n == "cli" || lib.hasPrefix "cli." n) (lib.attrNames cfg.settings);
in
{
  # Disable the upstream NixOS module to prevent conflicts
  disabledModules = [ "services/networking/orgscale.nix" ];

  options = {
    services.orgscale = {
      enable = lib.mkEnableOption "orgscale, Open Source coordination server for Tailscale";

      package = lib.mkPackageOption pkgs "orgscale" { };

      configFile = lib.mkOption {
        type = lib.types.path;
        readOnly = true;
        default = settingsFormat.generate "orgscale.yaml" cfg.settings;
        defaultText = lib.literalMD "{option}`services.orgscale.settings` as YAML";
        description = ''
          Path to the configuration file of orgscale:
          {option}`services.orgscale.settings` as YAML.
        '';
      };

      check.config = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          While building the system (native builds only), run
          `orgscale check config -f` on
          {option}`services.orgscale.configFile`, so a configuration orgscale
          rejects fails `nixos-rebuild build`.

          Before a switch restarts orgscale, run the new package's
          `orgscale check all` on the new configuration and abort the switch
          if it fails. It checks the configuration and every file it names
          (policy, DERP maps, extra records, TLS pair, existing noise and DERP
          keys) as the `orgscale` user. Keys are read, never created.

          Both run outside the unit, with its `environment` but not its
          `EnvironmentFile`; the switch check runs only when orgscale's unit
          changes. Neither reads a `file:` secret, so credentials referenced
          with `file:` pass. Turning off only
          {option}`services.orgscale.check.policy` does not skip the policy
          before a switch. Skip the switch check once with `NIXOS_NO_CHECK=1`.
        '';
      };

      check.policy = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          In file policy mode, run `orgscale check policy -f` on the new
          policy: it checks the policy and runs its `tests` and `sshTests`
          against one node for each user and tag the policy names. A policy in
          the Nix store is checked while building the system (native builds
          only), so a bad policy fails `nixos-rebuild build`. Any other policy
          is checked before a switch restarts orgscale.
          {option}`services.orgscale.check.config` checks the policy before a
          switch too; turn off both to skip every policy check.
        '';
      };

      # Read-only, kept only because nixpkgs' headplane module reads them.
      user = lib.mkOption {
        type = lib.types.str;
        default = "orgscale";
        readOnly = true;
        description = "User account under which orgscale runs, created by this module.";
      };

      group = lib.mkOption {
        type = lib.types.str;
        default = "orgscale";
        readOnly = true;
        description = "Group under which orgscale runs, created by this module.";
      };

      settings = lib.mkOption {
        description = ''
          orgscale's configuration, written to
          {option}`services.orgscale.configFile`. Each option is one key of
          [Configuration options](https://headscale.net/stable/ref/configuration-options/).
          A key without an option passes through to orgscale, which rejects
          unknown keys.
        '';
        type = lib.types.submodule {
          freeformType = settingsFormat.type;
          options = import ./options.generated.nix { inherit lib; };
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    system.checks =
      lib.optional buildTimePolicy policyCheck
      ++ lib.optional (cfg.check.config && canExecute) configCheck;

    system.preSwitchChecks.orgscale = lib.mkIf (
      cfg.check.config || preSwitchPolicy
    ) ''${lib.getExe preSwitchCheck} "$@"'';

    assertions = [
      {
        assertion = cliKeys == [ ];
        message = "services.orgscale.settings: remove ${lib.concatStringsSep ", " cliKeys}: the server never reads cli.*; orgscale's CLI reads ~/.config/orgscale/config.yaml or ORGSCALE_CLI_*, and here it would be written to the world-readable Nix store.";
      }
    ];

    services.orgscale.settings = lib.mapAttrsRecursive (_: distroDefault) {
      # Where NixOS keeps orgscale's state and socket. Nix pins the package,
      # so an update notice cannot be acted on.
      noise.private_key_path = "${dataDir}/noise_private.key";
      derp.server.private_key_path = "${dataDir}/derp_server_private.key";
      database.sqlite.path = "${dataDir}/db.sqlite";
      unix_socket = "${runDir}/orgscale.sock";
      disable_check_updates = true;
    };

    environment = {
      systemPackages = [ cfg.package ];

      # The local CLI reads no file: these point it at the server's socket and
      # update-check setting. Through PAM, so sudo keeps them too.
      sessionVariables = {
        ORGSCALE_UNIX_SOCKET = cfg.settings.unix_socket;
        ORGSCALE_DISABLE_CHECK_UPDATES = lib.boolToString cfg.settings.disable_check_updates;
      };
    };

    users.groups.orgscale = { };

    users.users.orgscale = {
      description = "orgscale user";
      home = dataDir;
      group = "orgscale";
      isSystemUser = true;
    };

    systemd.services.orgscale = {
      description = "orgscale coordination server for Tailscale";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      script = ''
        exec ${lib.getExe cfg.package} serve --config ${cfg.configFile}
      '';

      serviceConfig =
        let
          # "" empties the set, and comes first, so the unit gets only the
          # capabilities listed after it, here or by another module.
          capabilities = lib.mkBefore ([ "" ] ++ lib.optional privilegedPort "CAP_NET_BIND_SERVICE");
        in
        {
          ExecReload = "${pkgs.coreutils}/bin/kill -HUP $MAINPID";
          Restart = "always";
          RestartSec = "5s";
          # orgscale serve exits 78 (EX_CONFIG) on a configuration it rejects,
          # which a restart cannot fix.
          RestartPreventExitStatus = 78;
          Type = "simple";
          # Read here, so setting either read-only option fails evaluation.
          User = cfg.user;
          Group = cfg.group;

          # Hardening options
          RuntimeDirectory = "orgscale";
          # Allow orgscale group access so users can be added and use the CLI.
          RuntimeDirectoryMode = "0750";

          StateDirectory = "orgscale";
          StateDirectoryMode = "0750";

          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
          PrivateDevices = true;
          ProtectKernelTunables = true;
          ProtectControlGroups = true;
          RestrictSUIDSGID = true;
          PrivateMounts = true;
          ProtectKernelModules = true;
          ProtectKernelLogs = true;
          ProtectHostname = true;
          ProtectClock = true;
          ProtectProc = "invisible";
          ProcSubset = "pid";
          RestrictNamespaces = true;
          RemoveIPC = true;
          UMask = "0077";

          CapabilityBoundingSet = capabilities;
          AmbientCapabilities = capabilities;
          NoNewPrivileges = true;
          LockPersonality = true;
          RestrictRealtime = true;
          SystemCallFilter = [
            "@system-service"
            "~@privileged"
          ];
          SystemCallArchitectures = "native";
          RestrictAddressFamilies = "AF_INET AF_INET6 AF_UNIX";
        };
    };
  };

  meta.maintainers = with lib.maintainers; [
    kradalby
    misterio77
  ];
}
