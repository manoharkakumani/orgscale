# Makes a NixOS test node a orgscale control server that any Tailscale client
# joins without trust setup: plain-HTTP control, plus the same router on a
# self-signed TLS :443 whose DERP region is InsecureForTests. That is the one
# shape tailscaled, tsnet, webpki-only tailscale-rs and the Android app all
# accept. Contract: nix/README.md.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  hs-authkey = pkgs.writeShellApplication {
    name = "hs-authkey";
    runtimeInputs = [
      config.services.orgscale.package
      pkgs.jq
    ];
    text = ''
      user=''${1:?usage: hs-authkey USER [orgscale preauthkeys create flags]}
      shift
      # Safe straight after start_all(): the CLI retries the socket until this.
      ORGSCALE_CLI_TIMEOUT=''${ORGSCALE_CLI_TIMEOUT:-60s} orgscale health >/dev/null
      orgscale users list --name "$user" -o json | jq -e 'length > 0' >/dev/null ||
        orgscale users create "$user" >/dev/null
      orgscale preauthkeys create --user "$user" --reusable --expiration 24h "$@"
    '';
  };
in
{
  key = "orgscale-testkit";
  _file = ./testkit.nix;
  imports = [ self.nixosModules.orgscale ];

  services.orgscale = {
    enable = true;
    settings = {
      server_url = "http://${config.networking.hostName}";
      listen_addr = "[::]:80";
      dns.base_domain = lib.mkDefault "tailnet";
      dns.override_local_dns = lib.mkDefault false;
      # Nothing stored allows all, and `orgscale policy set` works live.
      policy.mode = lib.mkDefault "database";
      derp = {
        urls = [ ];
        server = {
          enabled = true;
          region_id = 999;
          region_code = "orgscale";
          region_name = "orgscale test kit";
          stun_listen_addr = "[::]:3478";
        };
      };
    };
  };

  # DERP rides this listener, and so does noise: Go clients redial only :443
  # for a hostname URL after a recent dial.
  systemd.services.orgscale = {
    environment.ORGSCALE_DEBUG_INSECURE_TLS_LISTEN_ADDR = "[::]:443";
    # The module grants this from listen_addr, which does not name this :443.
    serviceConfig.AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
    serviceConfig.CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
  };

  networking.firewall = {
    allowedTCPPorts = [
      80
      443
    ];
    allowedUDPPorts = [ 3478 ];
  };

  environment.systemPackages = [ hs-authkey ];
}
