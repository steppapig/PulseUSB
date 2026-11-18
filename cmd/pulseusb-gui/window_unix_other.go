//go:build !windows && !linux

package main

import "os/exec"

// On other Unix systems there is no window of our own; show the page in
// the default browser.
func openWindow(url string)   { exec.Command("open", url).Start() }
func runWindow(url string)    { openWindow(url) }
func windowLibsMissing() bool { return false }

const windowLibsHint = ""
