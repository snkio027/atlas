package main

import (
	"fmt"
	"github.com/snkio027/atlas/internal/webslice"
	"os"
)

func main() {
	if e := webslice.Run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
