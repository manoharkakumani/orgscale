# What the module writes, grants and exports, without a VM. orgscale's own
# check decides what the written config means.
self: pkgs:
let
  inherit (pkgs) lib;

  required = {
    server_url = "https://orgscale.example.com";
  };
  system =
    m:
    pkgs.nixos {
      imports = [
        self.nixosModules.default
        m
      ];
      services.orgscale = {
        enable = true;
        settings = required;
      };
    };
  settings = s: system { services.orgscale.settings = s; };
  # Only server_url: orgscale's defaults cover the rest.
  minimal = system { };
  # Between the module's 1200 and the options' defaults: a probe shows only
  # where the module stops setting a distro fact, even one equal to Go's.
  facts = settings (
    lib.mapAttrsRecursive (_: lib.mkOverride 1300) {
      noise.private_key_path = "/probe";
      derp.server.private_key_path = "/probe";
      database.sqlite.path = "/probe";
      unix_socket = "/probe";
      disable_check_updates = false;
    }
  );

  policy = pkgs.writeText "policy.json" "{}";
  derpMap = pkgs.writeText "derp.yaml" "regions: {}";

  okSettings = {
    policy = {
      mode = "file";
      path = policy;
    };
    derp.paths = [ derpMap ];
    unix_socket = "/run/orgscale/moved.sock";
    dns = {
      # Certificates need a domain you control.
      base_domain = "tailnet.example.com";
      certificates.enabled = true;
      challenge.provider = {
        name = "rfc2136";
        server = "127.0.0.1:53";
        key_name = "orgscale.";
        key_alg = "hmac-sha256.";
        key = "file:\${CREDENTIALS_DIRECTORY}/tsig";
      };
    };
  };
  ok = settings okSettings;
  http = system {
    services.orgscale = {
      settings.listen_addr = ":http";
      check.config = false;
    };
  };
  # A cross build cannot run the host's orgscale.
  crossWith =
    checkConfig:
    self.inputs.nixpkgs.lib.nixosSystem {
      modules = [
        self.nixosModules.default
        {
          nixpkgs.buildPlatform = "x86_64-linux";
          nixpkgs.hostPlatform = "aarch64-linux";
          services.orgscale = {
            enable = true;
            settings = lib.recursiveUpdate required okSettings;
            check.config = checkConfig;
          };
        }
      ];
    };
  cross = crossWith true;
  # With check.config off, only the store policy adds a switch check.
  crossPolicy = crossWith false;
  # With check.config off, check.policy alone must find the policy orgscale
  # loads: a non-empty ORGSCALE_POLICY_* of the unit over the settings.
  policyEnv =
    settings: env:
    system {
      services.orgscale = {
        inherit settings;
        check.config = false;
      };
      systemd.services.orgscale.environment = env;
    };
  storePolicy.policy = {
    mode = "file";
    path = policy;
  };
  mutablePolicy = "/var/lib/orgscale/policy.json";
  envOnly = policyEnv { } { ORGSCALE_POLICY_PATH = policy; };
  envMutable = policyEnv storePolicy { ORGSCALE_POLICY_PATH = mutablePolicy; };
  envDatabase = policyEnv storePolicy { ORGSCALE_POLICY_MODE = "database"; };
  envFile = policyEnv {
    policy = storePolicy.policy // {
      mode = "database";
    };
  } { ORGSCALE_POLICY_MODE = "file"; };
  # Empty means unset.
  envEmpty = policyEnv storePolicy {
    ORGSCALE_POLICY_MODE = null;
    ORGSCALE_POLICY_PATH = "";
  };
  # Activation then drops every group NixOS does not declare. systemd also
  # takes a GID and a space-separated string.
  immutableUsers = system {
    users.mutableUsers = false;
    systemd.services.orgscale.serviceConfig.SupplementaryGroups = [
      1234
      "a b"
    ];
  };
  # The build check sees the unit's environment, as the switch check does.
  badEnv = system {
    systemd.services.orgscale.environment = {
      ORGSCALE_SERVER_URL = "ftp://orgscale";
      # Any path literal and derivation: the check must see the unit's values.
      ORGSCALE_DNS_EXTRA_RECORDS_PATH = ./module.nix;
      ORGSCALE_POLICY_PATH = policy;
    };
  };
  # badEnv's ORGSCALE_* Environment= values, as nixpkgs renders the unit.
  badEnvUnit = map (l: builtins.fromJSON (lib.removePrefix "Environment=" l)) (
    lib.filter (lib.hasPrefix "Environment=\"ORGSCALE_") (
      lib.splitString "\n" (
        builtins.unsafeDiscardStringContext badEnv.config.systemd.units."orgscale.service".text
      )
    )
  );
  # A user's mkDefault and a plain assignment both replace the module's values.
  overrides = settings {
    noise.private_key_path = lib.mkDefault "/opt/orgscale/noise.key";
    unix_socket = "/run/orgscale/other.sock";
  };
  # Both spellings Go reads as cli.*.
  clis = settings {
    cli.api_key = "not-for-the-store";
    "cli.api_key" = "not-for-the-store";
  };
  # Key errors only: orgscale stops at them before it reads any value.
  bad = settings {
    listen_addr = "[::]:443";
    dns.magic_dsn = true;
    oidc.client_secret_path = "/run/x";
    db_password_file = "/run/x";
    tls_cert_path = "/x";
  };
  badUrl = settings { server_url = lib.mkForce "ftp://orgscale"; };
  # A record's unknown field is a decoding error, reported without the others.
  badRecord = settings {
    dns.extra_records = [
      {
        name = "grafana.tailnet.example.com";
        value = "100.64.0.3";
        typo = "A";
      }
    ];
  };
  # A system evaluated inside the build, where the test can read its error.
  evalSystem = pkgs.writeText "eval-system.nix" ''
    { case }:
    let
      inherit
        (import ${pkgs.path}/nixos/lib/eval-config.nix {
          system = "${pkgs.stdenv.hostPlatform.system}";
          modules = [
            ${self}/nix/module.nix
            (builtins.fromJSON (builtins.readFile case))
          ];
        })
        config
        ;
      unit = config.systemd.services.orgscale.serviceConfig;
    in
    # What building the system reads: the settings and the unit's account.
    builtins.deepSeq [
      config.services.orgscale.settings
      unit.User
      unit.Group
    ] true
  '';
  evalCase =
    name: c:
    pkgs.writeText "${name}.json" (
      builtins.toJSON (
        lib.recursiveUpdate {
          services.orgscale = {
            enable = true;
            settings = required;
          };
        } c
      )
    );

  configFile = s: s.config.services.orgscale.configFile;
  checkNames = s: map (d: d.name) s.config.system.checks;
  checkOf = name: s: lib.findFirst (d: d.name == name) (throw "no ${name}") s.config.system.checks;
  configCheckOf = checkOf "orgscale-config-check";
  # The build checks the store policy `policy`.
  buildsPolicy =
    s:
    lib.elem "orgscale-policy-check" (checkNames s)
    && lib.hasInfix (builtins.unsafeDiscardStringContext "${policy}") (checkOf "orgscale-policy-check" s)
    .buildCommand;
  switches = s: s.config.system.preSwitchChecks ? orgscale;
  switchScript = s: lib.head (lib.splitString " " s.config.system.preSwitchChecks.orgscale);
  binds =
    s:
    lib.elem "CAP_NET_BIND_SERVICE" s.config.systemd.services.orgscale.serviceConfig.CapabilityBoundingSet;
  inherit (ok.config.environment) sessionVariables;
  overridden = overrides.config.services.orgscale.settings;
in
assert lib.assertMsg (
  sessionVariables.ORGSCALE_UNIX_SOCKET or null == "/run/orgscale/moved.sock"
) "ok: ORGSCALE_UNIX_SOCKET is not settings.unix_socket";
assert lib.assertMsg (
  sessionVariables.ORGSCALE_DISABLE_CHECK_UPDATES or null == "true"
) "ok: ORGSCALE_DISABLE_CHECK_UPDATES is not true";
assert lib.assertMsg (
  overridden.noise.private_key_path == "/opt/orgscale/noise.key"
) "overrides: a user's mkDefault does not replace noise.private_key_path";
assert lib.assertMsg (
  overridden.unix_socket == "/run/orgscale/other.sock"
) "overrides: a plain assignment does not replace unix_socket";
assert lib.assertMsg (lib.any (
  a: !a.assertion && lib.hasPrefix "services.orgscale.settings: remove cli, cli.api_key:" a.message
) clis.config.assertions) "clis: no failed assertion names every spelling of settings.cli";
assert lib.assertMsg (
  !(ok.config.environment.etc ? "orgscale/config.yaml")
) "ok: /etc/orgscale/config.yaml is written";
assert lib.assertMsg (!binds ok) "ok: 0.0.0.0:8080 grants CAP_NET_BIND_SERVICE";
assert lib.assertMsg (binds http) "http: :http does not grant CAP_NET_BIND_SERVICE";
assert lib.assertMsg (binds bad) "bad: [::]:443 does not grant CAP_NET_BIND_SERVICE";
assert lib.assertMsg (
  ok.config.systemd.services.orgscale.serviceConfig.CapabilityBoundingSet == [ "" ]
) "ok: the unit keeps a capability";
assert lib.assertMsg (
  !lib.elem "orgscale-config-check" (checkNames http)
) "http: check.config = false still checks while building";
assert lib.assertMsg (lib.all (n: !lib.elem n (checkNames cross)) [
  "orgscale-config-check"
  "orgscale-policy-check"
]) "cross: a cross build runs a orgscale check";
assert lib.assertMsg (
  crossPolicy.config.system.preSwitchChecks ? orgscale
) "crossPolicy: the store policy is not checked before a switch";
assert lib.assertMsg (
  buildsPolicy envOnly && !switches envOnly
) "envOnly: the unit's ORGSCALE_POLICY_PATH is not checked while building";
assert lib.assertMsg (
  !lib.elem "orgscale-policy-check" (checkNames envMutable) && switches envMutable
) "envMutable: the settings' policy is checked instead of the unit's";
assert lib.assertMsg (
  !lib.elem "orgscale-policy-check" (checkNames envDatabase) && !switches envDatabase
) "envDatabase: ORGSCALE_POLICY_MODE=database still checks a policy";
assert lib.assertMsg (buildsPolicy envFile) "envFile: ORGSCALE_POLICY_MODE=file checks no policy";
assert lib.assertMsg (buildsPolicy envEmpty) "envEmpty: empty variables replace the settings";
assert lib.assertMsg (
  lib.length badEnvUnit == 3
) "badEnv: unit does not render 3 ORGSCALE_* values";
assert lib.assertMsg (lib.all (
  e: lib.hasInfix (lib.escapeShellArg e) (configCheckOf badEnv).buildCommand
) badEnvUnit) "badEnv: the build check's environment differs from the unit's";
pkgs.runCommand "orgscale-nixos-module"
  {
    # Built as an input: a config with a file: secret passes.
    okBuild = configCheckOf ok;
    minimalBuild = configCheckOf minimal;
    badBuild = pkgs.testers.testBuildFailure (configCheckOf bad);
    badEnvBuild = pkgs.testers.testBuildFailure (configCheckOf badEnv);
    nativeBuildInputs = [
      ok.config.services.orgscale.package
      pkgs.yq-go
      pkgs.jq
      pkgs.nix
    ];
  }
  ''
    want() {
      yq -o json "$1" | jq -e "$2" >/dev/null || { echo "$1: want $2"; cat "$1"; exit 1; }
    }
    rejects() {
      local file=$1 out
      shift
      if out=$(orgscale check config -f "$file" 2>&1); then
        echo "$file: check config passed"
        exit 1
      fi
      for msg in "$@"; do
        grep -qF -- "$msg" <<<"$out" || { echo "$file: no '$msg' in:"; echo "$out"; exit 1; }
      done
    }

    ok=${configFile ok}
    want $ok '.policy.path == "${policy}" and .derp.paths == ["${derpMap}"]'
    want $ok 'has("cli") | not'
    want $ok '.listen_addr == "0.0.0.0:8080" and .disable_check_updates == true'
    want $ok '.database.type == "sqlite"'
    want $ok '.derp.urls[0] == "https://controlplane.tailscale.com/derpmap/default"'
    want $ok '.dns.challenge.provider.key == "file:''${CREDENTIALS_DIRECTORY}/tsig"'
    want ${configFile facts} '.noise.private_key_path == "/var/lib/orgscale/noise_private.key"
      and .derp.server.private_key_path == "/var/lib/orgscale/derp_server_private.key"
      and .database.sqlite.path == "/var/lib/orgscale/db.sqlite"
      and .unix_socket == "/run/orgscale/orgscale.sock" and .disable_check_updates == true'
    orgscale check config -f $ok

    bad=${configFile bad}
    want $bad '(.dns | has("magic_dsn")) and (.oidc | has("client_secret_path"))'
    want $bad 'has("db_password_file") and has("tls_cert_path")'
    rejects $bad 'unknown configuration key dns.magic_dsn' \
      'oidc.client_secret_path was removed' \
      'db_password_file was removed' \
      'tls_cert_path was removed'

    rejects ${configFile badUrl} 'missing a scheme'

    badRecord=${configFile badRecord}
    want $badRecord '.dns.extra_records[0].typo == "A"'
    rejects $badRecord 'dns.extra_records has invalid keys: typo'

    built() {
      grep -qF -- "$2" "$1/testBuildFailure.log" || { echo "$1: no '$2' in:"; cat "$1/testBuildFailure.log"; exit 1; }
    }
    built $badBuild 'unknown configuration key dns.magic_dsn'
    built $badBuild 'oidc.client_secret_path was removed'
    built $badBuild 'services.orgscale.check.config = false skips it'
    built $badEnvBuild 'missing a scheme'

    switch=${switchScript envMutable}
    grep -qF -- ${lib.escapeShellArg "check policy -f ${lib.escapeShellArg mutablePolicy}"} $switch ||
      { echo "envMutable: switch check does not check ${mutablePolicy}:"; cat $switch; exit 1; }
    grep -qF 'id -Gn' ${switchScript ok} || { echo "ok: switch check drops current groups"; exit 1; }
    immutable=${switchScript immutableUsers}
    if grep -qF 'id -Gn' $immutable; then
      echo "immutableUsers: switch check keeps current groups"; exit 1
    fi
    grep -qF 'want=(orgscale 1234 a b)' $immutable ||
      { echo "immutableUsers: SupplementaryGroups not split:"; cat $immutable; exit 1; }

    export HOME=$TMPDIR NIX_STATE_DIR=$TMPDIR/state NIX_CONF_DIR=$TMPDIR/etc NIX_LOG_DIR=$TMPDIR/log
    evaluate() {
      nix-instantiate --store dummy:// --readonly-mode --eval --strict ${evalSystem} --argstr case "$1" 2>&1
    }
    evalFails() {
      local case=$1 out
      shift
      if out=$(evaluate "$case"); then
        echo "$case: evaluates"
        exit 1
      fi
      for msg in "$@"; do
        grep -qF -- "$msg" <<<"$out" || { echo "$case: no '$msg' in:"; echo "$out"; exit 1; }
      done
    }
    # The distro defaults pass their types.
    evaluate ${evalCase "defaults" { }} || exit 1
    evalFails ${
      evalCase "store-key" { services.orgscale.settings.noise.private_key_path = "${./module.nix}"; }
    } "services.orgscale.settings.noise.private_key_path' is not of type"
    evalFails ${
      evalCase "literal-secret" { services.orgscale.settings.oidc.client_secret = "hunter2"; }
    } "services.orgscale.settings.oidc.client_secret' is not of type"
    evalFails ${
      evalCase "no-port" { services.orgscale.settings.listen_addr = "0.0.0.0"; }
    } "services.orgscale.settings.listen_addr' is not of type"
    evalFails ${
      evalCase "user" { services.orgscale.user = "hs2"; }
    } "services.orgscale.user' is read-only"
    evalFails ${
      evalCase "group" { services.orgscale.group = "hs2"; }
    } "services.orgscale.group' is read-only"
    touch $out
  ''
