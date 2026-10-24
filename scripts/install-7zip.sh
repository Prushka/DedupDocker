#!/bin/sh
set -eu

architecture="${1:?Specify amd64 or arm64}"
prefix="${2:?Specify an absolute installation prefix}"
case "$prefix" in
  /*) ;;
  *) echo 'The installation prefix must be absolute.' >&2; exit 1 ;;
esac
case "$architecture" in
  amd64|x86_64|x64)
    archive=7z2605-linux-x64.tar.xz
    checksum=22cf82fcc6eb7474f1d5bb4f02c0f8ebd573a718089d6833dc30f8350a3b232e
    ;;
  arm64|aarch64)
    archive=7z2605-linux-arm64.tar.xz
    checksum=0b71f6cfc34669b3bf69f2347efd3429f77020665003a13b9cdc53b784270421
    ;;
  *) echo "Unsupported 7-Zip architecture: $architecture" >&2; exit 1 ;;
esac

temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM
curl --fail --location --retry 3 --output "$temporary/$archive" \
  "https://github.com/ip7z/7zip/releases/download/26.05/$archive"
printf '%s  %s\n' "$checksum" "$temporary/$archive" | sha256sum -c -
tar -xJf "$temporary/$archive" -C "$temporary" 7zzs License.txt
mkdir -p "$prefix/bin" "$prefix/share/licenses/7zip"
cp "$temporary/7zzs" "$prefix/bin/7zz"
chmod 0755 "$prefix/bin/7zz"
ln -sf 7zz "$prefix/bin/7z"
cp "$temporary/License.txt" "$prefix/share/licenses/7zip/License.txt"
