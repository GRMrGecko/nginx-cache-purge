package main

// Build identifiers populated at build time via -ldflags. A plain go build
// leaves them at their defaults, so the binary reports itself as a dev build.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
	Mode    = "dev"
)

// Application identifiers used by the CLI and the system service.
const (
	Name        = "nginx-cache-purge"
	DisplayName = "Nginx Cache Purge"
	Description = "Tool to help purge Nginx cache"
)
