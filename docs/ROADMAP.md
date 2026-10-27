# Roadmap

<p align="center">
  <a href="ROADMAP.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="ROADMAP.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>Language: click a badge to switch · 语言：点击徽章切换</sub></p>

Every milestone is committed and tagged (`v1.0`, `v2.0`, ...) before the next one starts.

| Version | Theme | Must include | Done when |
| --- | --- | --- | --- |
| **V1** | Usable: register / download / delete | Register (download and write `maio.toml`; register existing local GGUF in place), download (mirror, resume, checksums), delete (`rm` removes the model directory), minimal loop of `list/show/run/serve/ps/stop/boot` | A real model runs on real hardware; after `rm` the directory and config are gone; `make smoke` passes |
| **V2** | Understandable: errors + hardware awareness | Unified English error codes with actionable hints; config validation with line numbers; hardware probing (GPU/VRAM/CPU/RAM/disk); VRAM budget checks before loading (reject instead of OOM); GGUF capability probing (context/quantization/vision); `maio doctor`; JSONL audit log and `maio logs`; start-on-boot on by default; `sudo maio` as the only privilege syntax | Every failure explains cause and fix; hardware report matches `nvidia-smi`; budget rejections suggest alternatives; `make smoke` covers error codes, doctor and logs |
| **V3** | Dependable: server-grade stability and security | systemd supervision and crash recovery; concurrency and queueing; request auth; rate limiting; audit log; runtime metrics; upgrade and rollback; 24/7 operation | 24h soak with zero crashes and no leaks; unauthorized requests get 401; service and resident models recover after reboot |
| **V4** | Connected: distributed and LAN | mDNS node discovery; node registration; remote install and model distribution; cross-node inference routing; multi-node placement | Two machines set up in one pass; node A can call models on node B; reconnect does not disturb local service |
| **V5** | Complete: full control | Full hardware control (devices/VRAM/threads/context/batch size/quantization); graphical UI; bilingual error messages with command/config syntax hints (line and column) | All tuning possible without editing files; UI covers daily operations; errors are bilingual and locate the problem |

Upgrade rule: each version only adds what it must include. An update that does not improve stability,
security or speed is not made.
