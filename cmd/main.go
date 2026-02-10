package main

import (
	"fmt"
	"os"

	"dexianta/nci/tui"
)

func main() {
	if err := tui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui error:", err)
		os.Exit(1)
	}
}
