package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dotcommander/slither/internal/slither"
)

func main() {
	if err := slither.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
