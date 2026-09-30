package web

import "embed"

// Assets contains the minimal browser client.
//
//go:embed index.html app.js style.css
var Assets embed.FS
