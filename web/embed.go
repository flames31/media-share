// Package web embeds the server-rendered templates and static assets so the
// application ships as a single self-contained binary.
package web

import "embed"

//go:embed templates/*.html
var TemplatesFS embed.FS

//go:embed static/*
var StaticFS embed.FS
