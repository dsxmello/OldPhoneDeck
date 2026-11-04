// Package web embeds the phone UI so the Go server ships as a single binary.
// It lives next to index.html so the Swift (macOS) build and the Go build share one copy of the page.
package web

import _ "embed"

//go:embed index.html
var IndexHTML []byte
