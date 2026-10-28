# maio apt repository

Flat apt repository for `maio-cluster` on Debian/Ubuntu amd64.

## Install

```sh
curl -fsSL https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-apt@apt-v4.1.1/setup.sh | sudo sh
```

or manually:

```sh
echo 'deb [trusted=yes] https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-apt@apt-v4.1.1 ./' \
  | sudo tee /etc/apt/sources.list.d/maio.list
sudo apt update
sudo apt install maio-cluster
```

The repository is unsigned; `[trusted=yes]` is deliberate for a small
LAN/competition deployment. For production, mirror and sign it.

## Upgrade

```sh
sudo apt update && sudo apt install --only-upgrade maio-cluster
```

`maio update` detects a dpkg-owned binary and points at this command instead
of replacing files behind apt's back. `maio update --force` bypasses that.

## After install

```sh
sudo maio --system web install       # optional web console
sudo maio --system token show        # web login token
sudo maio --system cluster join-code # worker pairing
```

Models and plugins are **not** apt packages; after maio is installed use the
maio package manager:

```sh
sudo maio --system pull qwen2.5-0.5b
sudo maio --system install hello
```

## Layout

```text
Packages                                  # flat repository index
Release                                   # unsigned metadata
maio-cluster_<version>_amd64.deb          # package
setup.sh                                  # source + install helper
```
