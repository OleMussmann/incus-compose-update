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
          incus-compose-update = pkgs.writeShellApplication {
            name = "incus-compose-update";
            runtimeInputs = with pkgs; [
              git
              coreutils
              gnused
              gnugrep
              jq
              yq
              curl
              incus
            ];
            text = builtins.readFile ./bin/incus-compose-update;
          };

          default = self.packages.${system}.incus-compose-update;
        }
      );
    };
}
