//go:build tools

package main

// Keep the dynamically passed gomobile package in the module graph. The
// builder names it as a command argument, which go mod tidy cannot otherwise
// discover from imports.
import _ "github.com/openlibrecommunity/olcrtc/mobile"
