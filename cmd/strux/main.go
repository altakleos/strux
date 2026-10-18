// Command strux is the Strux command line.
package main

import (
	"context"
	"os"

	"github.com/altakleos/strux/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
