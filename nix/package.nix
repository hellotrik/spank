{ lib, buildGoModule, go_1_26 }:

buildGoModule.override { go = go_1_26; } {
  pname = "spank";
  version = "0-unstable";

  src = lib.cleanSource ./..;

  # Run `nix build` to get the correct hash, then replace this placeholder
  vendorHash = "sha256-R63lSTQvwZ/zw2ccuwXk6rkuQ0t2Zs4mU3Trh+IxKf4=";

  ldflags = [ "-s" "-w" ];

  doCheck = false;

  meta = {
    description = "Plays audio on left mouse button release";
    homepage = "https://github.com/taigrr/spank";
    license = lib.licenses.mit;
    platforms = [ "aarch64-darwin" ];
    mainProgram = "spank";
  };
}
