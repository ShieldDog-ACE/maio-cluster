# maio v4 on Linux

The Linux product tree is self-contained: it builds the `maio` binary, bundles
the llama.cpp RPC runtime, and ships a Debian package, a tarball and a source
installer.

## Install

```bash
# Debian / Ubuntu
sudo apt install ./dist/maio-cluster_4.1.0_amd64.deb
sudo maio --system cluster join-code      # print a pairing code
sudo maio --system cluster nodes          # members
journalctl -u maio -f                     # logs
```

The package creates the `maio` service user, initializes a cluster identity on
first install and starts `maio cluster up` through systemd. Model data lives in
`/var/lib/maio/models`, configuration in `/etc/maio/cluster.toml`.

Source install:

```bash
VERSION=4.1.0 RPC_BIN=/path/ggml-rpc-server LLAMA_BIN=/path/llama-server \
  ./linux/build.sh
sudo bash dist/linux-amd64/install.sh
```

Worker only:

```bash
sudo bash dist/linux-amd64/install.sh --worker
sudo maio --system cluster join 'MAIO1-...'
sudo systemctl restart maio
```

The generated service runs the agent; join writes the coordinator seed and
token. `maio service install --worker` does the same on an already installed
machine.

## Runtime layout

```
/usr/bin/maio
/usr/lib/maio/bin/llama-server
/usr/lib/maio/bin/ggml-rpc-server
/etc/maio/cluster.toml
/var/lib/maio/models/
/var/log/maio/
```

## Accelerators

The bundled `llama-server` and `ggml-rpc-server` are CPU builds so the package
works on every x86-64 machine. For NVIDIA layer shards, build llama.cpp with
the CUDA backend on a build host and point `agent.rpc_bin` / `agent.llama_bin`
at the resulting binaries:

```bash
cmake -S llama.cpp -B build-rpc-cuda -DGGML_CUDA=ON -DGGML_RPC=ON
cmake --build build-rpc-cuda -j
```

The frontend (`llama-server`) and every RPC worker must both be built with
`-DGGML_RPC=ON`. CPU workers still work and can be mixed with GPU workers.

## Ports

| Port | Protocol | Purpose |
| --- | --- | --- |
| 47800 | TCP | coordinator API |
| 47801 | UDP | discovery beacon |
| 47802 | TCP | reverse tunnel for NAT/firewall fallback |
| 50100+ | TCP | ggml-rpc-server on workers |

On a firewalled host allow `47800-47802/tcp` and `47801/udp`; RPC ports are
only needed when direct LAN connections are used (the coordinator falls back to
the reverse tunnel automatically).
