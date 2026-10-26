package main

import (
	"os"

	"github.com/ShieldDog-ACE/maio-cluster/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
