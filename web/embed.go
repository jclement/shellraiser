// Package web holds the embedded shellraiser front-end assets.
package web

import "embed"

//go:embed index.html app.js logo.png jbmono-regular.woff2 jbmono-bold.woff2
var Assets embed.FS
