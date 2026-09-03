#!/usr/bin/env bash
# Publish .deb files into a signed, static apt repository tree (one suite).
#
#   scripts/apt-repo.sh <suite> <site-dir> <deb>...
#
# Layout under <site-dir> (served as-is, e.g. by GitHub Pages):
#   pool/<suite>/main/s/s2u/<pkg>.deb
#   dists/<suite>/{Release,Release.gpg,InRelease}
#   dists/<suite>/main/binary-<arch>/Packages{,.gz}
# Only the given .debs remain in the suite's pool (latest build per suite);
# earlier builds stay on GitHub Releases. Other suites are left untouched.
# Signing uses the default gpg key of the current GNUPGHOME (set APT_GPG_KEY_ID
# to pick one). Requires dpkg-dev + apt-utils + gpg.
set -euo pipefail
suite="${1:?suite (stable|beta)}"; site="${2:?site dir}"; shift 2
[ "$#" -ge 1 ] || { echo "no .deb files given" >&2; exit 2; }
case "$suite" in stable|beta) ;; *) echo "suite must be stable or beta" >&2; exit 2;; esac

pool="pool/$suite/main/s/s2u"
dist="dists/$suite"
rm -rf "$site/$pool" "$site/$dist"
mkdir -p "$site/$pool"
cp "$@" "$site/$pool/"

archs=""
for deb in "$site/$pool"/*.deb; do
  a="$(dpkg-deb -f "$deb" Architecture)"
  case " $archs " in *" $a "*) ;; *) archs="$archs $a";; esac
done
archs="${archs# }"

cd "$site"
for a in $archs; do
  mkdir -p "$dist/main/binary-$a"
  # Filename: paths are relative to the repo root, hence scanning from here.
  dpkg-scanpackages --arch "$a" "$pool" /dev/null > "$dist/main/binary-$a/Packages"
  gzip -9 -k -f "$dist/main/binary-$a/Packages"
done

apt-ftparchive \
  -o "APT::FTPArchive::Release::Origin=Share2Us" \
  -o "APT::FTPArchive::Release::Label=Share2Us" \
  -o "APT::FTPArchive::Release::Suite=$suite" \
  -o "APT::FTPArchive::Release::Codename=$suite" \
  -o "APT::FTPArchive::Release::Architectures=$archs" \
  -o "APT::FTPArchive::Release::Components=main" \
  -o "APT::FTPArchive::Release::Description=Share2Us command line ($suite)" \
  release "$dist" > "$dist/Release"

keyopt=()
[ -n "${APT_GPG_KEY_ID:-}" ] && keyopt=(--local-user "$APT_GPG_KEY_ID")
gpg --batch --yes "${keyopt[@]}" --armor --detach-sign --output "$dist/Release.gpg" "$dist/Release"
gpg --batch --yes "${keyopt[@]}" --clearsign --output "$dist/InRelease" "$dist/Release"
echo "apt repo: suite=$suite archs=[$archs] debs=$(ls "$pool" | tr '\n' ' ')"
