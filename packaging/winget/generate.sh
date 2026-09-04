#!/usr/bin/env bash
# Generate the three winget manifest files for a release into
# packaging/winget/manifests/<VERSION>/. The Windows release assets
# (share2us_windows_<arch>.zip) are the installers; the exe inside is registered
# as the `s2u` and `share2us` portable command aliases. No new artifact.
#
#   VERSION=20260904061335 packaging/winget/generate.sh
#
# SHA256s come from the release .sha256 sidecars (needs gh + a token).
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:?set VERSION (the release tag without the v)}"
REPO="${WINGET_CLI_REPO:-share2us/cli}"
ID="Share2Us.CLI"
BASE="https://github.com/${REPO}/releases/download/v${VERSION}"

sha() { gh release download "v${VERSION}" --repo "$REPO" -p "$1.sha256" -O - 2>/dev/null | tr '[:lower:]' '[:upper:]' | tr -d '[:space:]'; }
AMD64_SHA="$(sha share2us_windows_amd64.zip)"
ARM64_SHA="$(sha share2us_windows_arm64.zip)"
[ -n "$AMD64_SHA" ] && [ -n "$ARM64_SHA" ] || { echo "missing sha256 sidecars for the windows zips" >&2; exit 1; }

OUT="manifests/${VERSION}"
mkdir -p "$OUT"

cat > "$OUT/${ID}.installer.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.1.6.0.schema.json
PackageIdentifier: ${ID}
PackageVersion: "${VERSION}"
MinimumOSVersion: 10.0.0.0
InstallerType: zip
NestedInstallerType: portable
Commands:
  - s2u
  - share2us
NestedInstallerFiles:
  - RelativeFilePath: share2us.exe
    PortableCommandAlias: s2u
  - RelativeFilePath: share2us.exe
    PortableCommandAlias: share2us
Installers:
  - Architecture: x64
    InstallerUrl: ${BASE}/share2us_windows_amd64.zip
    InstallerSha256: ${AMD64_SHA}
  - Architecture: arm64
    InstallerUrl: ${BASE}/share2us_windows_arm64.zip
    InstallerSha256: ${ARM64_SHA}
ManifestType: installer
ManifestVersion: 1.6.0
YAML

cat > "$OUT/${ID}.locale.en-US.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.1.6.0.schema.json
PackageIdentifier: ${ID}
PackageVersion: "${VERSION}"
PackageLocale: en-US
Publisher: Share2Us
PublisherUrl: https://share2.us
PublisherSupportUrl: https://github.com/${REPO}/issues
PackageName: Share2Us CLI
PackageUrl: https://share2.us
License: MIT
LicenseUrl: https://github.com/${REPO}/blob/main/LICENSE
ShortDescription: Share files and folders by link, to your devices, or directly over the local network.
Description: |-
  The Share2Us command line (s2u): share files and folders by link, send them to
  your other devices or contacts end-to-end encrypted, or transfer directly over
  the local network. Installs the s2u and share2us commands.
Tags:
  - file-sharing
  - cli
  - p2p
  - encryption
ManifestType: defaultLocale
ManifestVersion: 1.6.0
YAML

cat > "$OUT/${ID}.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.1.6.0.schema.json
PackageIdentifier: ${ID}
PackageVersion: "${VERSION}"
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.6.0
YAML

echo "wrote $OUT/{${ID}.yaml,${ID}.installer.yaml,${ID}.locale.en-US.yaml}"
