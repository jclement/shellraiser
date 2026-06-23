// Package web holds the embedded shellraiser front-end assets.
package web

import "embed"

//go:embed index.html app.js
var Assets embed.FS
