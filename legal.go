// Package ainovel exposes the license texts embedded in desktop distributions.
package ainovel

import "embed"

// LegalFiles keeps the project's license and third-party notices available offline.
//
//go:embed LICENSE NOTICE LICENSES
var LegalFiles embed.FS
