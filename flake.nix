{
  description = "orgscale - Open Source Tailscale Control server";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Reusable Go flake checks (build/test/lint/format); CI runs them via
    # `nix build .#checks.<system>.<name>` instead of bespoke per-tool steps.
    flake-checks.url = "github:kradalby/flake-checks";
    flake-checks.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      flake-checks,
      ...
    }:
    let
      orgscaleVersion = self.shortRev or self.dirtyShortRev;
      commitHash = self.rev or self.dirtyRev;
    in
    {
      # NixOS module
      nixosModules = rec {
        # Defaults the package to this flake's: the pre-switch checks need
        # `orgscale check`. Priority 1001 sits just below mkDefault, so a
        # user's mkDefault wins instead of conflicting. The overlay, not
        # self.packages, so every system nixpkgs supports evaluates.
        orgscale =
          { lib, pkgs, ... }:
          {
            # Dedupes importing it twice, directly and via testkit.
            key = "${toString ./flake.nix}#nixosModules.orgscale";
            _file = "${toString ./flake.nix}#nixosModules.orgscale";
            imports = [ ./nix/module.nix ];
            services.orgscale.package = lib.mkOverride 1001 (self.overlays.default pkgs pkgs).orgscale;
          };
        default = orgscale;
        # Control node for NixOS VM tests of Tailscale clients, and a peer
        # that joins it; nix/README.md.
        testkit = import ./nix/testkit.nix self;
        testkit-peer = ./nix/testkit-peer.nix;
      };

      overlays.default =
        _: prev:
        let
          pkgs = nixpkgs.legacyPackages.${prev.stdenv.hostPlatform.system};
          # Tracks the newest Go in nixpkgs so a Go release bump is a
          # flake.lock update, not a flake.nix edit.
          buildGo = pkgs.buildGoLatestModule;
          vendorHash = (builtins.fromJSON (builtins.readFile ./flakehashes.json)).vendor.sri;
        in
        {
          orgscale = buildGo {
            pname = "orgscale";
            version = orgscaleVersion;
            src = pkgs.lib.cleanSource self;

            # Only run unit tests when testing a build
            checkFlags = [ "-short" ];

            # vendorHash is read from flakehashes.json; refresh via:
            #   go run ./cmd/vendorhash update
            inherit vendorHash;

            subPackages = [ "cmd/orgscale" ];

            meta = {
              mainProgram = "orgscale";
            };
          };

          hi = buildGo {
            pname = "hi";
            version = orgscaleVersion;
            src = pkgs.lib.cleanSource self;

            checkFlags = [ "-short" ];
            inherit vendorHash;

            subPackages = [ "cmd/hi" ];
          };

          gotestsum = prev.gotestsum.override {
            buildGoModule = buildGo;
          };

          gotests = prev.gotests.override {
            buildGoModule = buildGo;
          };

          gofumpt = prev.gofumpt.override {
            buildGoModule = buildGo;
          };

          # goimports and friends: they parse Go with the parser of the Go
          # they were built with, so an older one rejects new syntax.
          gotools = prev.gotools.override {
            buildGoModule = buildGo;
            # goimports is wrapped with this go on PATH for module lookups.
            go = pkgs.go_latest;
          };

          golangci-lint-langserver = prev.golangci-lint-langserver.override {
            buildGoModule = buildGo;
          };
        };
    }
    # Explicit: nixpkgs no longer evaluates x86_64-darwin, which
    # eachDefaultSystem still lists.
    // flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ] (
      system:
      let
        pkgs = import nixpkgs {
          overlays = [ self.overlays.default ];
          inherit system;
        };
        buildDeps = with pkgs; [
          git
          go_latest
          gnumake
        ];
        devDeps =
          with pkgs;
          buildDeps
          ++ [
            golangci-lint
            golangci-lint-langserver
            prettier
            nixfmt
            goreleaser
            nfpm
            gotestsum
            gotests
            gofumpt
            gopls
            gotools

            gh
            gh-actions-lock
            ksh
            ko
            yq-go
            ripgrep
            postgresql

            # External clients exercised by the Tailscale-compatible v2 API
            # roundtrip tests (TestAPIv2). Binaries: tofu, tscli.
            opentofu
            tscli
            python3Packages.mdformat
            python3Packages.mdformat-footnote
            python3Packages.mdformat-frontmatter
            python3Packages.mdformat-mkdocs
            prek

            # 'dot' is needed for pprof graphs
            # go tool pprof -http=: <source>
            graphviz
          ]
          ++ lib.optionals pkgs.stdenv.hostPlatform.isLinux [ traceroute ];

        # Add entry to build a docker image with orgscale
        # caveat: only works on Linux
        #
        # Usage:
        # nix build .#orgscale-docker
        # docker load < result
        orgscale-docker = pkgs.dockerTools.buildLayeredImage {
          name = "orgscale";
          tag = orgscaleVersion;
          # CA certificates for outbound HTTPS, such as the DERP map.
          contents = [
            pkgs.orgscale
            pkgs.cacert
          ];
          config.Entrypoint = [ (pkgs.orgscale + "/bin/orgscale") ];
          config.Env = [ "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt" ];
        };

        # Go flake checks from the flake-checks library. CI gates on
        # `nix build .#checks.<system>.<name>`; the logic lives here, not in
        # bespoke workflow steps. Linux-only: parts of the tree are
        # Linux-specific and the pure unit subset is validated by CI.
        fc = flake-checks.lib;
        common = {
          inherit pkgs;
          root = ./.;
          pname = "orgscale";
          version = orgscaleVersion;
          vendorHash = (builtins.fromJSON (builtins.readFile ./flakehashes.json)).vendor.sri;
          goPkg = pkgs.go_latest;
          # //go:embed targets and test-read files outside the default whitelist.
          embedDirs = [
            ./hscontrol/assets
            ./hscontrol/adminui/dist
            ./hscontrol/db/schema.sql
          ];
          extraSrc = [
            ./hscontrol/testdata
            ./hscontrol/config/testdata
            ./hscontrol/db/testdata
            ./hscontrol/policy/v2/testdata
          ];
        };
        goChecks = {
          build = fc.goBuild (common // { subPackages = [ "cmd/orgscale" ]; });

          # The pure unit subset. The root ./integration package (Docker) and
          # ./hscontrol/servertest (slow: 10s+ convergence plus race/stress/HA
          # property tests — run by the servertest workflow instead) are dropped
          # from the test set but kept in source so cmd/hi and friends still
          # compile. Pure integration helper tests remain included.
          # TestPostgres* needs a server (the SQLite equivalents still
          # run). This check keeps CGO off; gorace below enables it for tests.
          gotest = fc.goTest (
            common
            // {
              testExclude = [
                "/integration$"
                "/hscontrol/servertest"
              ];
              goSkip = [ "TestPostgres" ];
              testEnv = "export CGO_ENABLED=0";
            }
          );

          # -race needs cgo; release build and dev shell stay CGO-free.
          # Test hscontrol recursively; servertest runs in its own workflow, without -race.
          gorace = fc.goTest (
            common
            // {
              name = "orgscale-gorace";
              goRace = true;
              testEnv = "export CGO_ENABLED=1";
              testFlags = [ "-p=2" ];
              goSkip = [ "TestPostgres" ];
              testPackages = "./hscontrol/...";
              testExclude = [ "/hscontrol/servertest" ];
            }
          );

          # Full-tree golangci-lint (gofumpt, gci, etc.); uses the overlay's
          # golangci-lint built against the pinned Go.
          golangci-lint = fc.goLint common;

          # nixfmt + prettier, excluding generated output. goFmt = "off":
          # Go formatting (gofumpt, gci) is enforced by the golangci-lint
          # check, not treefmt. prettierExts matches the old prettier-lint glob
          # (no json: testdata fixtures are hand-formatted).
          formatting = fc.goFormat (
            common
            // {
              goFmt = "off";
              prettier = true;
              prettierExts = [
                "ts"
                "js"
                "md"
                "yaml"
                "yml"
                "sass"
                "css"
                "scss"
                "html"
              ];
              # Mirror .prettierignore (docs/ are mkdocs-flavoured; gen/ generated).
              # hscontrol/adminui/dist is the console's build output.
              fmtExclude = [
                ./gen
                ./docs
                ./hscontrol/adminui/dist
              ];
            }
          );
        };

        # nix/options.generated.nix: what the binary prints, every default
        # and example of its option's type, the examples loaded unchanged by
        # orgscale, and descriptions that render.
        nixos-options =
          let
            inherit (pkgs) lib;
            options = import ./nix/options.generated.nix { inherit lib; };
            each = f: lib.mapAttrsRecursiveCond (o: !lib.isOption o) (_: f) options;
            eval = config: lib.evalModules { modules = [ { inherit options config; } ]; };
            defaults = eval (each (o: lib.mkIf (!(o ? default)) o.example));
            examples = eval (each (o: o.example));
            doc = pkgs.nixosOptionsDoc { inherit (examples) options; };
            # Development tool, not shipped: loads a file as orgscale does.
            files = pkgs.orgscale.overrideAttrs { subPackages = [ "tools/configgen/files" ]; };
            # Values of the wrong shape: each must fail the option it is set on.
            rejected = {
              listen_addr = [
                "8080"
                "::1:8080"
                "0.0.0.0:abc"
              ];
              "node.expiry" = [
                "30"
                ""
                "3 hours"
              ];
              unix_socket_permission = [
                "0778"
                "07777"
                504
              ];
              "prefixes.v4" = [
                "100.64.0.1"
                "100.64.0.0/33"
              ];
              "prefixes.v6" = [ "fd7a::/129" ];
              "oidc.issuer" = [
                "orgscale.example.com"
                "ftp://x"
                "https://"
              ];
              "oidc.client_secret" = [ "hunter2" ];
              "database.postgres.pass" = [ "hunter2" ];
              # Relative to the config, which is in the store.
              "policy.path" = [
                "policy.hujson"
                42
              ];
              "tls.cert_path" = [ "cert.pem" ];
              "derp.paths" = [ [ "derp.yaml" ] ];
              # A key in the store is world-readable.
              "noise.private_key_path" = [
                ./flake.nix
                "${./flake.nix}"
                "noise_private.key"
              ];
              "tls.key_path" = [ "${./flake.nix}" ];
              "database.sqlite.path" = [ "db.sqlite" ];
              "dns.extra_records" = map (r: [ ({ name = "grafana.example.com"; } // r) ]) [
                { value = "grafana"; }
                { value = "256.1.1.1"; }
                {
                  type = "CNAME";
                  value = "100.64.0.3";
                }
                { }
              ];
            };
            # Only the option itself is forced, so nothing else can fail it.
            fails =
              name: value:
              let
                path = lib.splitString "." name;
                config = (eval (lib.setAttrByPath path value)).config;
              in
              lib.hasAttrByPath path options
              && !(builtins.tryEval (builtins.deepSeq (lib.getAttrFromPath path config) null)).success;
            accepted = lib.concatLists (
              lib.mapAttrsToList (
                name: values: map (v: "${name} = ${builtins.toJSON v}") (lib.filter (v: !fails name v) values)
              ) rejected
            );
          in
          assert lib.assertMsg (
            accepted == [ ]
          ) "nixos-options: types accept ${lib.concatStringsSep "; " accepted}";
          pkgs.runCommand "orgscale-nixos-options"
            {
              nativeBuildInputs = [
                pkgs.orgscale
                pkgs.yq-go
                pkgs.jq
              ];
              # Unused; evaluating it checks every default against its type.
              defaults = builtins.toJSON defaults.config;
              examples = builtins.toJSON examples.config;
              passAsFile = [ "examples" ];
            }
            ''
              orgscale generate nixos-options | diff -u ${./nix/options.generated.nix} -
              cp "$examplesPath" examples.json
              ${files}/bin/files -load examples.json > loaded.yaml
              jq -S . examples.json > want.json
              yq -o json . loaded.yaml | jq -S . > got.json
              diff -u want.json got.json
              # A secret's NixOS form renders as Nix source takes it.
              grep -F '"file:\''${CREDENTIALS_DIRECTORY}/oidc_client_secret"' ${doc.optionsCommonMark}
              touch $out
            '';
      in
      {
        # `nix develop`
        devShells.default = pkgs.mkShell {
          buildInputs = devDeps ++ [
            (pkgs.writeShellScriptBin "nix-vendor-sri" ''
              set -eu
              exec go run ./cmd/vendorhash update "$@"
            '')

            (pkgs.writeShellScriptBin "go-mod-update-all" ''
              cat go.mod | ${pkgs.ripgrep}/bin/rg "\t" | ${pkgs.ripgrep}/bin/rg -v '^\s*//' | ${pkgs.ripgrep}/bin/rg -v indirect | ${pkgs.gawk}/bin/awk '{print $1}' | ${pkgs.findutils}/bin/xargs go get -u
              go mod tidy
            '')
          ];

          shellHook = ''
            export PATH="$PWD/result/bin:$PATH"
            export CGO_ENABLED=0
          '';
        };

        # `nix build`
        packages = with pkgs; {
          inherit orgscale;
          inherit orgscale-docker;
          default = orgscale;
        };

        # `nix run`
        apps.orgscale = flake-utils.lib.mkApp {
          drv = pkgs.orgscale;
        };
        apps.default = flake-utils.lib.mkApp {
          drv = pkgs.orgscale;
        };

        # `nix fmt` is built from the same treefmt module as the formatting
        # check, so the two cannot disagree. Every formatter the tree is judged
        # by lives here, not in a hand-kept list of pre-commit hooks.
        formatter = fc.formatter (
          common
          // {
            goFmt = "off";
            prettier = true;
          }
        );

        # Gated to Linux: parts of the tree are Linux-specific, and the VM
        # tests need KVM.
        checks = pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux (
          goChecks
          // {
            orgscale = pkgs.testers.runNixOSTest (import ./nix/tests/orgscale.nix self);
            orgscale-checks = pkgs.testers.runNixOSTest (import ./nix/tests/checks.nix self);
            nixos-module = import ./nix/tests/module.nix self pkgs;
            inherit nixos-options;
          }
        );
      }
    );
}
