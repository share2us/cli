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
  const osName = { linux: "linux", darwin: "darwin", win32: "windows" }[process.platform];
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!osName || !arch) {
    fail(
      `unsupported platform ${process.platform}/${process.arch}. ` +
        "Build from source (https://github.com/share2us/cli) or use https://share2.us/install.sh"
    );
  }
  // Windows releases ship share2us.exe in a .zip; every other platform ships a
  // bare binary in a .tar.gz.
  const isWindows = osName === "windows";
  return {
    archive: `share2us_${osName}_${arch}.${isWindows ? "zip" : "tar.gz"}`,
    binaryInArchive: isWindows ? "share2us.exe" : "share2us",
    binaryOnDisk: isWindows ? "share2us-bin.exe" : "share2us-bin",
    isWindows,
  };
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

// Integrity check. Unix mirrors install.sh (system cksum + .crc32 sidecar);
// Windows has no cksum, so it verifies the .sha256 sidecar with Node's crypto.
async function verifyIntegrity(archivePath, base, isWindows) {
  try {
    if (isWindows) {
      const sidecar = archivePath + ".sha256";
      await download(base + ".sha256", sidecar);
      const crypto = require("crypto");
      const got = crypto
        .createHash("sha256")
        .update(fs.readFileSync(archivePath))
        .digest("hex");
      const want = fs.readFileSync(sidecar, "utf8").trim().split(/\s+/)[0];
      if (got.toLowerCase() !== String(want).toLowerCase()) {
        fail(`SHA-256 check failed for ${path.basename(archivePath)}`);
      }
    } else {
      const crc = archivePath + ".crc32";
      await download(base + ".crc32", crc);
      const got = execFileSync("cksum", [archivePath]).toString().trim().split(/\s+/);
      const want = fs.readFileSync(crc, "utf8").trim().split(/\s+/);
      if (got[0] !== want[0] || got[1] !== want[1]) {
        fail(`CRC check failed for ${path.basename(archivePath)}`);
      }
    }
  } catch (e) {
    if (String(e && e.message).includes("check failed")) throw e;
    // sidecar/tool unavailable: HTTPS already protects the transfer.
  }
}

async function main() {
  const { archive, binaryInArchive, binaryOnDisk, isWindows } = platform();
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "s2u-install-"));
  const archivePath = path.join(tmp, archive);
  const base = assetURL(archive);

  console.log(`share2us: downloading ${archive} (${VERSION})...`);
  await download(base, archivePath);

  await verifyIntegrity(archivePath, base, isWindows);

  // Extract the binary and place it beside the shim. Unix uses system tar on
  // the .tar.gz; Windows uses PowerShell's Expand-Archive on the .zip (always
  // present, unlike a modern bsdtar).
  if (isWindows) {
    execFileSync("powershell", [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      `Expand-Archive -Force -LiteralPath '${archivePath}' -DestinationPath '${tmp}'`,
    ]);
  } else {
    execFileSync("tar", ["-xf", archivePath, "-C", tmp]);
  }
  const src = path.join(tmp, binaryInArchive);
  if (!fs.existsSync(src)) fail(`archive did not contain ${binaryInArchive}`);
  const binDir = path.join(__dirname, "bin");
  fs.mkdirSync(binDir, { recursive: true });
  const out = path.join(binDir, binaryOnDisk);
  fs.copyFileSync(src, out);
  if (!isWindows) fs.chmodSync(out, 0o755);
  fs.rmSync(tmp, { recursive: true, force: true });

  console.log("share2us: installed. Run `s2u login` to get started.");
}

main().catch((e) => fail((e && e.message) || String(e)));
