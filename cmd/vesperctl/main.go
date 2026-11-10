// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

// Command vesperctl is the command-line tool for Vesper.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}
