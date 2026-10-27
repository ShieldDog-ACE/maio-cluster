// Package version carries the single build version of maio v4.
package version

// Version is overridden at build time with
// -ldflags "-X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=..."
var Version = "4.0.0-dev"

// Protocol is the cluster wire protocol version. Peers with a different major
// version must not join the same cluster.
const Protocol = 1

// Product is the user-facing product name.
const Product = "maio"
