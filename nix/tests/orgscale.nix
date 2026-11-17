# The test kit's contract test, and CI's coverage of nix/module.nix.
self:
{ lib, pkgs, ... }:
let
  policy = pkgs.writeText "policy.json" (
    builtins.toJSON {
      acls = [
        {
          action = "accept";
          src = [ "*" ];
          dst = [ "*:*" ];
        }
      ];
    }
  );
in
{
  name = "orgscale";
  meta.maintainers = with lib.maintainers; [
    kradalby
    misterio77
  ];

  nodes = {
    orgscale = {
      imports = [ self.nixosModules.testkit ];
      services.orgscale.settings.dns = {
        extra_records = [
          {
            name = "foo.bar";
            type = "A";
            value = "100.64.0.2";
          }
        ];
        # Startup builds the provider but does not call it.
        certificates.enabled = true;
        challenge.provider = {
          name = "rfc2136";
          server = "127.0.0.1:53";
          key_name = "orgscale.";
          key_alg = "hmac-sha256.";
          key = "file:\${CREDENTIALS_DIRECTORY}/tsig";
        };
      };
      # Read by serve, never by the checks.
      systemd.services.orgscale.serviceConfig.LoadCredential = [
        "tsig:${pkgs.writeText "tsig" "c2VjcmV0"}"
      ];
    };
    # All of peer1's noise goes over the kit's TLS listener, not plain :80.
    peer1 = {
      imports = [ self.nixosModules.testkit-peer ];
      systemd.services.tailscaled.environment.TS_FORCE_NOISE_443 = "1";
    };
    # No direct UDP for peer2: its traffic has to cross the kit's DERP.
    peer2 = {
      imports = [ self.nixosModules.testkit-peer ];
      systemd.services.tailscaled.environment.TS_DEBUG_ALWAYS_USE_DERP = "1";
    };
  };

  testScript = ''
    from datetime import timedelta

    start_all()
    key = orgscale.succeed("hs-authkey test").strip()
    for peer in [peer1, peer2]:
        peer.succeed(f"hs-join {key}")

    peer1.wait_until_succeeds("tailscale ping --until-direct=false -c 1 peer2 | grep -F 'via DERP(orgscale)'")
    peer2.wait_until_succeeds("tailscale ping --until-direct=false -c 1 peer1.tailnet")
    res = peer1.wait_until_succeeds("${lib.getExe pkgs.dig} +short foo.bar").strip()
    assert res == "100.64.0.2", res
    peer1.wait_until_succeeds(
        "tailscale status --json | ${lib.getExe pkgs.jq} -e '.CertDomains == [\"peer1.tailnet\"]'"
    )

    orgscale.succeed("orgscale policy set -f ${policy}")
    # The default socket needs neither a config file nor a variable.
    orgscale.succeed("env -u ORGSCALE_UNIX_SOCKET orgscale nodes list")
    # sudo resets the environment; PAM sets the module's variables again.
    out = orgscale.succeed("env -u ORGSCALE_DISABLE_CHECK_UPDATES sudo printenv ORGSCALE_DISABLE_CHECK_UPDATES")
    assert out.strip() == "true", out

    # The docs tell operators to run this; it needs ExecReload in the unit.
    orgscale.succeed("systemctl reload orgscale.service")
    orgscale.wait_until_succeeds("journalctl -u orgscale.service --grep 'Received SIGHUP'")

    # Clients reconnect after a control restart, peer1 over the TLS listener.
    orgscale.systemctl("restart orgscale.service")
    orgscale.wait_until_succeeds(
        "orgscale nodes list -o json | ${lib.getExe pkgs.jq} -e 'length == 2 and all(.[]; .online)'",
        timeout=timedelta(minutes=2),
    )
    peer1.wait_until_succeeds("tailscale ping --until-direct=false -c 1 peer2")
  '';
}
