# Third-party software notices

The root [Apache-2.0 license](LICENSE) applies to this workbench's original code. It does not replace the separate licenses of the software and assets listed below.

## infinite-canvas

- Upstream: [tigerowo/infinite-canvas](https://github.com/tigerowo/infinite-canvas)
- Pinned revision: [`ccda172efba25cbee990a785bba715b90cc42b95`](https://github.com/tigerowo/infinite-canvas/tree/ccda172efba25cbee990a785bba715b90cc42b95)
- Source location: [`third_party/infinite-canvas/`](third_party/infinite-canvas/)
- License: [GNU Affero General Public License v3.0](third_party/infinite-canvas/LICENSE), identified by upstream as AGPL-3.0-only.
- Attribution: tigerowo/infinite-canvas and its contributors. Upstream acknowledges basketikun and HuFakai; the original README, notices and page branding are retained.
- Provenance and file digests: [`AINOVEL_UPSTREAM.json`](third_party/infinite-canvas/AINOVEL_UPSTREAM.json).

The upstream application is retained as a separate source module and runs as a separate service. This workbench exchanges user-requested chapter export files with it; its Go packages and frontend components are not linked into the workbench executable. No upstream application source file has been edited in this snapshot. Runtime-installed dependencies, locally generated build outputs and the optional prebuilt 3D director resources described below are not included.

Keep the upstream copyright and license notices when redistributing its source. When distributing built versions, provide the applicable Corresponding Source under AGPL-3.0; when modifying it and offering the modified version over a network, provide that version's Corresponding Source as required by section 13. This repository includes the application source snapshot; a future integrated or modified distribution must assess its actual combination and distribution terms. The directory boundary alone is not a license exemption. See the [license text](https://www.gnu.org/licenses/agpl-3.0.en.html) and the [upstream license guidance](third_party/infinite-canvas/docs/business/license.md).

### Excluded optional 3D director resources

The upstream repository supplies prebuilt `web/public/director/index.html` and its two JS/CSS bundles, but omits the director's original build project (the upstream `.gitignore` excludes `/director/`). These three prebuilt files are not included in this source-only integration because their complete corresponding source is not present in the supplied repository. All excluded paths and reasons are recorded in the provenance manifest. The optional 3D director is consequently unavailable in this snapshot; the 2D canvas and chapter ZIP exchange do not depend on it.

The upstream binary `web/public/director/models/ue-mannequin-retopology.glb` is also **not included**. Its adjacent [original asset notice](third_party/infinite-canvas/web/public/director/models/ue-mannequin-retopology.license.txt) identifies William Luque's “UE Mannequin (Retopology)” under the [Sketchfab Standard license](https://sketchfab.com/licenses), which restricts standalone redistribution. No additional permission to redistribute that raw binary was supplied. Its notice and exclusion entry are retained for traceability.

Dependencies installed by the independent upstream application's Go/Bun build retain their respective licenses. They are not vendored or relicensed by this notice.
