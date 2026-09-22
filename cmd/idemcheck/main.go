package main

import (
	"os"

	"github.com/idemcheck/idemcheck/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
