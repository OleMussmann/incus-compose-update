{
  description = "incus-compose-update — generic incus-compose image updater";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          incus-compose-update = pkgs.buildGoModule {
            pname = "incus-compose-update";
            version = "0.1.0";
            src = self;
            vendorHash = "sha256-lliVSewCk9SwUQhiqsXUhXvDd1d9ymDWKu9sSAyZJhI=";
            subPackages = [ "cmd/incus-compose-update" ];
            env.CGO_ENABLED = "0";
            ldflags = [ "-s" "-w" ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            # gitsync's tests drive real repositories.
            nativeCheckInputs = [ pkgs.git ];
            # The default check phase tests only subPackages.
            checkPhase = ''
              runHook preCheck
              go vet ./...
              go test ./...
              runHook postCheck
            '';
            # git and ssh for pulling and pushing stack checkouts; incus-compose
            # itself is expected on PATH.
            postInstall = ''
              wrapProgram $out/bin/incus-compose-update \
                --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.git pkgs.openssh ]}
            '';
          };

          default = self.packages.${system}.incus-compose-update;
        }
      );
    };
}
