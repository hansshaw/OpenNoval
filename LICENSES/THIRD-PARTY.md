# Third-party notices

The application as a whole is AGPL-3.0-only. Third-party components keep their
original licenses; the retained license files are authoritative.

## Go dependencies

License and notice files copied unmodified from the exact versions in go.sum:

| Module | Version | Notices |
| --- | --- | --- |
| `github.com/davecgh/go-spew` | `v1.1.1` | [LICENSE](go/github.com__davecgh__go-spew@v1.1.1/LICENSE) |
| `github.com/gofrs/flock` | `v0.13.0` | [LICENSE](go/github.com__gofrs__flock@v0.13.0/LICENSE) |
| `github.com/pmezard/go-difflib` | `v1.0.0` | [LICENSE](go/github.com__pmezard__go-difflib@v1.0.0/LICENSE) |
| `github.com/stretchr/testify` | `v1.11.1` | [LICENSE](go/github.com__stretchr__testify@v1.11.1/LICENSE) |
| `github.com/voocel/agentcore` | `v1.8.3` | [LICENSE](go/github.com__voocel__agentcore@v1.8.3/LICENSE) |
| `github.com/voocel/litellm` | `v1.8.10` | [LICENSE](go/github.com__voocel__litellm@v1.8.10/LICENSE) |
| `golang.org/x/image` | `v0.45.0` | [LICENSE](go/golang.org__x__image@v0.45.0/LICENSE), [PATENTS](go/golang.org__x__image@v0.45.0/PATENTS) |
| `golang.org/x/mod` | `v0.40.0` | [LICENSE](go/golang.org__x__mod@v0.40.0/LICENSE), [PATENTS](go/golang.org__x__mod@v0.40.0/PATENTS) |
| `golang.org/x/net` | `v0.58.0` | [LICENSE](go/golang.org__x__net@v0.58.0/LICENSE), [PATENTS](go/golang.org__x__net@v0.58.0/PATENTS) |
| `golang.org/x/sys` | `v0.47.0` | [LICENSE](go/golang.org__x__sys@v0.47.0/LICENSE), [PATENTS](go/golang.org__x__sys@v0.47.0/PATENTS) |
| `golang.org/x/text` | `v0.41.0` | [LICENSE](go/golang.org__x__text@v0.41.0/LICENSE), [PATENTS](go/golang.org__x__text@v0.41.0/PATENTS) |
| `gopkg.in/yaml.v3` | `v3.0.1` | [NOTICE](go/gopkg.in__yaml.v3@v3.0.1/NOTICE), [LICENSE](go/gopkg.in__yaml.v3@v3.0.1/LICENSE) |

## PDF.js and browser runtime

PDF.js 6.4.299 is Apache-2.0; its font, CMap, ICC and codec notices remain
with the vendored assets under `internal/entry/desktopui/static/vendor/pdfjs/`.
An offline copy of those notices is also provided in [pdfjs/](pdfjs/LICENSE).
`desktop/scripts/sync-reader-vendor.js` verifies the pinned package and hashes.

Electron is MIT; Chromium and other runtime components retain their upstream
licenses. Electron's LICENSE and LICENSES.chromium.html are distributed by the
Electron packaging tooling with the runtime. npm build tools are development
dependencies; versions are fixed in desktop/package-lock.json.

The inherited source attribution and original Apache-2.0 license are preserved
in the root NOTICE and this directory. No third-party attribution is removed
by the repository's Git-history cleanup.
