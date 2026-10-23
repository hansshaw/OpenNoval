"use strict";

const path = require("node:path");
const { Arch } = require("electron-builder");
const { buildSidecar } = require("./build-sidecar");

module.exports = async function beforePack(context) {
  // Match extraResources' ${os}/${arch} macros, keeping each target isolated.
  buildSidecar({
    platform: context.electronPlatformName,
    arch: Arch[context.arch],
    outputDir: path.join(
      context.packager.projectDir,
      "bin",
      context.packager.platform.buildConfigurationKey,
      Arch[context.arch],
    ),
  });
};
