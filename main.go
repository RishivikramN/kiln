package main

import (
	"fmt"
	"os"
)

func main() {
	tui := NewTUI()
	if err := tui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}