// Package web holds the embedded slopbox front-end assets.
package web

import "embed"

//go:embed index.html app.js
var Assets embed.FS
