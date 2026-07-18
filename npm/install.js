#!/usr/bin/env node
// postinstall: download the prebuilt Share2Us CLI binary for this platform from
// GitHub Releases (built by CI for every platform) and place it next to the shim.
// Mirrors https://share2.us/install.sh (system tar + cksum; no runtime deps).
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const { execFileSync } = require("child_process");

const REPO = process.env.SHARE2US_INSTALL_REPO || "share2us/cli";

// The release this npm version maps to. CI pins `share2usCliVersion` in
// package.json to the matching release tag at publish time, so a given npm
// version always installs the same binary. Falls back to the latest release.
function pinnedVersion() {
  try {
    return require("./package.json").share2usCliVersion || "latest";
  } catch (e) {
    return "latest";
  }
}
const VERSION = process.env.SHARE2US_VERSION || pinnedVersion();

function fail(msg) {
  console.error("share2us install: " + msg);
  process.exit(1);
}

function platform() {
  const osName = { linux: "linux", darwin: "darwin" }[process.platform];
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!osName || !arch) {
    fail(
      `unsupported platform ${process.platform}/${process.arch}. ` +
        "Build from source (https://github.com/share2us/cli) or use https://share2.us/install.sh"
    );
  }
  return { archive: `share2us_${osName}_${arch}.tar.gz` };
}

function assetURL(archive) {
  return VERSION === "latest"
    ? `https://github.com/${REPO}/releases/latest/download/${archive}`
    : `https://github.com/${REPO}/releases/download/${VERSION}/${archive}`;
}

function download(url, dest, redirects = 0) {
  if (redirects > 8) return Promise.reject(new Error("too many redirects"));
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "share2us-npm-installer" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume();
          resolve(download(res.headers.location, dest, redirects + 1));
          return;
        }
        if (res.statusCode !== 200) {
          res.resume();
          reject(new Error(`HTTP ${res.statusCode} for ${url}`));
          return;
        }
        const out = fs.createWriteStream(dest);
        res.pipe(out);
        out.on("finish", () => out.close(() => resolve()));
        out.on("error", reject);
      })
      .on("error", reject);
  });
}

async function main() {
  const { archive } = platform();
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "s2u-install-"));
  const tgz = path.join(tmp, archive);
  const crc = tgz + ".crc32";
  const base = assetURL(archive);

  console.log(`share2us: downloading ${archive} (${VERSION})...`);
  await download(base, tgz);

  // Integrity: system cksum against the .crc32 sidecar (same as install.sh).
  try {
    await download(base + ".crc32", crc);
    const got = execFileSync("cksum", [tgz]).toString().trim().split(/\s+/);
    const want = fs.readFileSync(crc, "utf8").trim().split(/\s+/);
    if (got[0] !== want[0] || got[1] !== want[1]) {
      fail(`CRC check failed for ${archive}`);
    }
  } catch (e) {
    if (String(e && e.message).includes("CRC check failed")) throw e;
    // sidecar/cksum unavailable: HTTPS already protects the transfer.
  }

  // Extract the `share2us` binary and place it beside the shim.
  execFileSync("tar", ["-xzf", tgz, "-C", tmp]);
  const src = path.join(tmp, "share2us");
  if (!fs.existsSync(src)) fail("archive did not contain the share2us binary");
  const binDir = path.join(__dirname, "bin");
  fs.mkdirSync(binDir, { recursive: true });
  const out = path.join(binDir, "share2us-bin");
  fs.copyFileSync(src, out);
  fs.chmodSync(out, 0o755);
  fs.rmSync(tmp, { recursive: true, force: true });

  console.log("share2us: installed. Run `s2u login` to get started.");
}

main().catch((e) => fail((e && e.message) || String(e)));
