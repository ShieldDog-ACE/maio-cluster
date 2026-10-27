//go:build windows

package main

import (
	"os"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cli"
	windows "github.com/ShieldDog-ACE/maio-cluster/v4/windows/platform"
)

func main() {
	os.Exit(cli.Run(windows.New(), os.Args[1:]))
}
