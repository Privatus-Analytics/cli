module github.com/privatus-analytics/cli

// A patched release of a supported Go line: the binary carries the
// standard library (TLS, HTTP) that sends your API token. Older toolchains
// refuse to build it, or fetch this one when GOTOOLCHAIN allows.
go 1.26.8
