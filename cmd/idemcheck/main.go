package main

import (
	"os"

	"github.com/hyukvoid/idemcheck/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
