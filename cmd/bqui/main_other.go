//go:build !darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bqui: the GUI only runs on macOS; use bquictl for headless use")
	os.Exit(2)
}
