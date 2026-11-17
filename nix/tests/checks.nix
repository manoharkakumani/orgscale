# A policy orgscale rejects must fail the build, and a config or policy it
# rejects must fail a switch before orgscale restarts.
self:
{ lib, pkgs, ... }:
let
  writePolicy = name: policy: pkgs.writeText name (builtins.toJSON policy);
  acceptAll = [
    {
      action = "accept";
      src = [ "*" ];
      dst = [ "*:*" ];
    }
  ];
  valid = writePolicy "valid.json" { acls = acceptAll; };
  # group:missing is never defined.
  undefinedGroup = writePolicy "undefined-group.json" {
    acls = [
      {
        action = "accept";
        src = [ "group:missing" ];
        dst = [ "*:*" ];
      }
    ];
  };
  # Stand-in users and nodes decide these tests.
  testsPass = writePolicy "tests-pass.json" {
    acls = acceptAll;
    tests = [
      {
        src = "test@";
        accept = [ "test@:22" ];
      }
    ];
  };
  testsFail = writePolicy "tests-fail.json" {
    acls = acceptAll;
    tests = [
      {
        src = "test@";
        deny = [ "test@:22" ];
      }
    ];
  };

  # The build-time check a system with this store-path policy must pass.
  policyBuild =
    policy:
    lib.findFirst (d: d.name == "orgscale-policy-check") (throw "no orgscale-policy-check")
      (pkgs.nixos {
        imports = [ self.nixosModules.orgscale ];
        services.orgscale = {
          enable = true;
          settings.policy = {
            mode = "file";
            path = policy;
          };
        };
      }).config.system.checks;
  buildFailures = {
    undefinedGroup = pkgs.testers.testBuildFailure (policyBuild undefinedGroup);
    testsFail = pkgs.testers.testBuildFailure (policyBuild testsFail);
  };

  # Outside the store, so only the switch can check it.
  mutablePolicy = "/var/lib/orgscale/policy.json";
  # Readable only through hshand, a group made by hand.
  handGroupPolicy = "/var/lib/hshand/policy.json";
  # Readable only through the group hsread.
  groupPolicy = "/var/lib/hsread/policy.json";
  # Written broken by the test: only the switch reads named files.
  badRecords = "/var/lib/orgscale/extra-records.json";
  badServerUrl = lib.mkForce "ftp://orgscale";
in
{
  name = "orgscale-checks";
  meta.maintainers = with lib.maintainers; [ kradalby ];

  nodes.orgscale = {
    imports = [
      self.nixosModules.testkit
      # Specialisations that set more than services.orgscale.
      {
        specialisation = lib.mapAttrs (_: c: { configuration = c; }) {
          # A bad value here would fail the build check, which sees it too.
          unitEnv.systemd.services.orgscale.environment.ORGSCALE_DNS_EXTRA_RECORDS_PATH = badRecords;
          # hsnew is created by this switch, so the check leaves it out.
          supplementaryGroup = {
            services.orgscale.settings.policy.path = lib.mkForce groupPolicy;
            users.groups.hsnew = { };
            systemd.services.orgscale.serviceConfig.SupplementaryGroups = [
              "hsread"
              "hsnew"
            ];
          };
          extraGroup = {
            services.orgscale.settings.policy.path = lib.mkForce groupPolicy;
            users.users.orgscale.extraGroups = [ "hsread" ];
          };
          memberGroup = {
            services.orgscale.settings.policy.path = lib.mkForce groupPolicy;
            users.groups.hsread.members = [ "orgscale" ];
          };
        };
      }
    ];
    services.orgscale.settings = {
      policy = {
        mode = "file";
        path = valid;
      };
      # The CLI calls below find it through ORGSCALE_UNIX_SOCKET.
      unix_socket = "/run/orgscale/moved.sock";
    };
    users.groups.hsread = { };
    specialisation = lib.mapAttrs (_: c: { configuration.services.orgscale = c; }) {
      badRecords.settings.dns.extra_records_path = badRecords;
      mutable.settings.policy.path = lib.mkForce mutablePolicy;
      policyOnly = {
        settings.policy.path = lib.mkForce mutablePolicy;
        check.config = false;
      };
      # Builds only because check.config = false also drops the build check;
      # orgscale itself refuses it.
      unchecked = {
        settings.server_url = badServerUrl;
        check = {
          config = false;
          policy = false;
        };
      };
      testsPass.settings.policy.path = lib.mkForce testsPass;
      # Activation keeps a membership of a group NixOS does not declare.
      handGroup.settings.policy.path = lib.mkForce handGroupPolicy;
      credential.settings.dns = {
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
  };

  testScript =
    { nodes, ... }:
    let
      toplevel = nodes.orgscale.system.build.toplevel;
      jq = lib.getExe pkgs.jq;
    in
    ''
      from datetime import timedelta

      def stc(spec, action):
          return f"${toplevel}/specialisation/{spec}/bin/switch-to-configuration {action} 2>&1"

      def main_pid():
          return orgscale.succeed("systemctl show -P MainPID orgscale.service").strip()

      def served_policy():
          return orgscale.succeed("orgscale policy get | ${jq} -S .")

      def put_policy(src, mode="644"):
          orgscale.succeed(f"install -m {mode} {src} ${mutablePolicy}")

      def assert_rejected(spec, why):
          out = orgscale.fail(stc(spec, "test"))
          assert "Pre-switch check 'orgscale' failed" in out, out
          assert why in out, out
          assert "NIXOS_NO_CHECK=1" in out, "no skip hint:\n" + out
          return out

      with subtest("a bad store-path policy fails the build"):
          for path, why in [
              ("${buildFailures.undefinedGroup}", 'group not found: "group:missing"'),
              ("${buildFailures.testsFail}", "test(s) failed"),
          ]:
              build_log = open(f"{path}/testBuildFailure.log").read()
              assert why in build_log, build_log

      orgscale.wait_for_unit("orgscale.service")
      orgscale.wait_until_succeeds("orgscale health", timeout=timedelta(seconds=60))
      orgscale.fail("test -e /etc/orgscale/config.yaml")
      # sudo resets the environment; PAM sets ORGSCALE_UNIX_SOCKET again.
      orgscale.succeed("env -u ORGSCALE_UNIX_SOCKET sudo -u orgscale orgscale health")

      with subtest("rejected switches leave the running orgscale alone"):
          pid = main_pid()
          before = served_policy()
          orgscale.succeed("echo '{' > ${badRecords}")
          assert_rejected("badRecords", "dns.extra_records_path")
          assert_rejected("unitEnv", "dns.extra_records_path")
          put_policy("${undefinedGroup}")
          assert_rejected("mutable", 'group not found: "group:missing"')
          put_policy("${testsFail}")
          assert_rejected("mutable", "test(s) failed")
          out = assert_rejected("policyOnly", "test(s) failed")
          assert "checking the new configuration" not in out, out
          # Checked as the orgscale user, who cannot read this file.
          put_policy("${valid}", "600")
          assert_rejected("mutable", "permission denied")
          assert main_pid() == pid, "orgscale was restarted"
          assert served_policy() == before, "old policy no longer served"
          orgscale.succeed("orgscale health")

      with subtest("a passing switch check"):
          put_policy("${valid}")
          # The caller's environment does not reach the check.
          orgscale.succeed("ORGSCALE_SERVER_URL=ftp://orgscale " + stc("mutable", "check"))

      with subtest("the check has the unit's groups"):
          orgscale.succeed("install -d -m 750 -g hsread ${builtins.dirOf groupPolicy}")
          orgscale.succeed("install -m 640 -g hsread ${valid} ${groupPolicy}")
          orgscale.succeed(stc("supplementaryGroup", "check"))
          orgscale.succeed(stc("extraGroup", "check"))
          orgscale.succeed(stc("memberGroup", "check"))
          orgscale.succeed("groupadd hshand && usermod -aG hshand orgscale")
          orgscale.succeed("install -d -m 750 -g hshand ${builtins.dirOf handGroupPolicy}")
          orgscale.succeed("install -m 640 -g hshand ${valid} ${handGroupPolicy}")
          orgscale.succeed(stc("handGroup", "check"))

      with subtest("a file: secret is skipped, not read"):
          out = orgscale.succeed(stc("credential", "check"))
          assert "secret, not read" in out, out

      with subtest("skips"):
          out = orgscale.succeed("${toplevel}/bin/switch-to-configuration check 2>&1")
          assert "unit unchanged" in out, out

      with subtest("escape hatches"):
          orgscale.succeed(stc("unchecked", "check"))
          orgscale.succeed("NIXOS_NO_CHECK=1 " + stc("badRecords", "check"))

      with subtest("a valid switch goes through and applies the policy"):
          pid = main_pid()
          orgscale.succeed(stc("testsPass", "test"))
          orgscale.wait_until_succeeds("orgscale health", timeout=timedelta(seconds=60))
          assert main_pid() != pid, "orgscale was not restarted"
          want = orgscale.succeed("${jq} -S . ${testsPass}")
          assert served_policy() == want, "new policy not served"

      with subtest("a configuration orgscale rejects is not restarted"):
          # The switch may report the unit failed; the unit is what counts.
          orgscale.execute(stc("unchecked", "test"))
          orgscale.wait_until_succeeds("systemctl is-failed orgscale.service", timeout=timedelta(seconds=60))
          status = orgscale.succeed("systemctl show -P ExecMainStatus orgscale.service").strip()
          assert status == "78", f"exit status {status}, want 78"
          restarts = orgscale.succeed("systemctl show -P NRestarts orgscale.service")
          # Past RestartSec.
          orgscale.sleep(10)
          orgscale.succeed("systemctl is-failed orgscale.service")
          assert orgscale.succeed("systemctl show -P NRestarts orgscale.service") == restarts, "orgscale was restarted"

      # Last: it takes orgscale's account away.
      with subtest("an account activation has yet to create skips the checks"):
          orgscale.succeed("systemctl stop orgscale.service")
          orgscale.succeed("groupmod -n hsgone orgscale")
          out = orgscale.succeed(stc("mutable", "check"))
          assert "group orgscale does not exist yet" in out, out
          orgscale.succeed("usermod -l hsgone orgscale")
          out = orgscale.succeed(stc("mutable", "check"))
          assert "user orgscale does not exist yet" in out, out
    '';
}
