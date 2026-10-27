# maio v4 on Windows

The Windows product builds a single `maio.exe` and a PowerShell installer. It
runs as an ordinary console program under Task Scheduler (no service wrapper
DLL), so it needs no third-party runtime.

## Build

On Windows with Go 1.24+ and PowerShell 5.1+:

```powershell
# optional: build llama.cpp with RPC first
cmake -S llama.cpp -B build-rpc -DGGML_RPC=ON
cmake --build build-rpc --config Release

.\windows\build.ps1 -RpcBin .\build-rpc\bin\Release\ggml-rpc-server.exe `
                    -LlamaBin .\build-rpc\bin\Release\llama-server.exe
```

The output is `dist\maio-windows-amd64.zip`. The Go binary can also be
cross-compiled on Linux/macOS:

```bash
GOOS=windows GOARCH=amd64 go build -o dist/maio.exe ./windows/cmd/maio
```

## Install

```powershell
Expand-Archive maio-windows-amd64.zip -DestinationPath maio
cd maio
.\install.ps1                 # per-user coordinator + agent
.\install.ps1 -Worker         # worker only
.\install.ps1 -System         # machine-wide, elevated PowerShell
.\install.ps1 -System -JoinCode "MAIO1-..."
```

The installer copies the runtime to `%LOCALAPPDATA%\maio` (or
`%ProgramFiles%\maio`), updates PATH, creates the scheduled task `maio`,
starts it, and adds firewall rules for system scope. The task runs at logon
(per-user) or at boot as SYSTEM (machine-wide).

Paths:

```
%APPDATA%\maio\cluster.toml                 (user config)
%LOCALAPPDATA%\maio\models\                 (user models)
%ProgramData%\maio\cluster.toml             (system config)
%ProgramData%\maio\models\                  (system models)
```

## Notes

- Windows may show a Defender Firewall prompt the first time; allow private
  networks. System install adds rules for TCP 47800-47802 and UDP 47801.
- `maio link usb` requires `adb.exe` from Android platform-tools. It runs
  `adb reverse` so the APK can use `http://127.0.0.1:47800` over the cable.
- Build the RPC runtime with `-DGGML_RPC=ON`; the frontend and every worker
  must use RPC-enabled binaries. CUDA builds need the CUDA toolkit.
- Scheduled tasks avoid the Windows SCM 1053 problem that a plain console
  program has when registered with `sc.exe`.
