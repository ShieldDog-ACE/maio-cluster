//go:build darwin

package main

import (
	"os"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cli"
	mac "github.com/ShieldDog-ACE/maio-cluster/v4/mac/platform"
)

func main() {
	os.Exit(cli.Run(mac.New(), os.Args[1:]))
}
