// Package web serves the same assets edited in this directory.
package web

import "embed"

//go:embed models.html models.js models.css accounts.js radar.html radar.js radar.css chrome.js memory.html memory.js memory.css theme.css question.html question.js question.css vendor index.html app.js style.css conversation.html conversation.js conversation.css profile.html profile.css profile.js
var Files embed.FS
