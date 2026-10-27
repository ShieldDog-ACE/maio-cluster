//go:build linux

package main

import (
	"os"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cli"
	linux "github.com/ShieldDog-ACE/maio-cluster/v4/linux/platform"
)

func main() {
	os.Exit(cli.Run(linux.New(), os.Args[1:]))
}
