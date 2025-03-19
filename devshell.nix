{ pkgs }:
pkgs.mkShell {
  packages = [
    pkgs.go
    pkgs.gopls
    pkgs.go-tools
    pkgs.gotools
    pkgs.golangci-lint
    pkgs.deno
  ];
}
