#!/usr/bin/env node
// Thin launcher: exec the native Share2Us CLI binary that install.js placed here.
"use strict";

const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");

const bin = path.join(__dirname, "share2us-bin");

if (!fs.existsSync(bin)) {
  console.error("share2us: the CLI binary is missing (install scripts may have been skipped).");
  console.error("Reinstall with `npm i -g @share2us/cli`, or run:");
  console.error("  node " + path.join(__dirname, "..", "install.js"));
  process.exit(1);
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (res.error) {
  console.error("share2us: " + res.error.message);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
