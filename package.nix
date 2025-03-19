{ pname, pkgs }:
pkgs.buildGoModule {
  pname = "clouddns";
  version = "1.0.0";

  src = ./.;

  vendorHash = null;

  meta = {
    description = "clo4's Cloudflare DDNS client";
    homepage = "github.com/clo4/clouddns";
    license = pkgs.lib.licenses.mit;
  };
}
