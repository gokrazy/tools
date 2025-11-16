{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils.url = "github:numtide/flake-utils";
    rpi-eeprom-tools = {
      url = "github:info-beamer/rpi-eeprom-tools";
      flake = false;
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      rpi-eeprom-tools,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # Python environment with pycryptodome for pi-eeprom-tools
        pythonEnv = pkgs.python312.withPackages (
          ps: with ps; [
            pycryptodome
          ]
        );

        # Helper to create a wrapper for a pi-eeprom-* script
        mkPiEepromTool =
          name:
          pkgs.writeShellApplication {
            inherit name;
            runtimeInputs = [
              pythonEnv
            ];
            text = ''
              exec ${pythonEnv.interpreter} ${rpi-eeprom-tools}/${name} "$@"
            '';
          };

        # All the pi-eeprom tools
        pi-eeprom-extract = mkPiEepromTool "pi-eeprom-extract";
        pi-eeprom-ls = mkPiEepromTool "pi-eeprom-ls";
        pi-eeprom-recompress = mkPiEepromTool "pi-eeprom-recompress";
        pi-eeprom-update = mkPiEepromTool "pi-eeprom-update";
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            raspberrypi-eeprom
            pi-eeprom-extract
            pi-eeprom-ls
            pi-eeprom-recompress
            pi-eeprom-update
          ];

          shellHook = ''
            echo "Nix-based gokrazy/tools dev env (go: $(go version))"
          '';
        };
      }
    );
}
