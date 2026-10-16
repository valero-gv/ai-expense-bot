// Package web contains the embedded expense dashboard.
package web

import "embed"

// Files is the browser application served by the HTTP API.
//
//go:embed index.html app.js style.css
var Files embed.FS
