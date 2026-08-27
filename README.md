<div align="right"><sub>[English](./README.en.md)&nbsp;&nbsp;⇄&nbsp;&nbsp;<b>简体中文</b></sub></div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./assets/hero-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="./assets/hero-light.svg">
  <img src="./assets/hero-light.svg" width="880" alt="hflock">
</picture>

<p align="center"><sub>国产模型权重镜像与哈希溯源 CLI —— 把 DeepSeek/Qwen/GLM 权重从 Hugging Face 镜像到 Gitee AI/ModelScope，并生成可机器校验的 SHA256 溯源清单。</sub></p>

<p align="center">
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="license"></a>
  <a href="https://github.com/SuperMarioYL/hflock/releases"><img src="https://img.shields.io/github/v/release/SuperMarioYL/hflock?label=release" alt="release"></a>
  <img src="https://img.shields.io/github/actions/workflow/status/SuperMarioYL/hflock/ci.yml?branch=main&label=CI" alt="CI">
  <img src="https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white" alt="Go">
</p>

<p align="center"><b>CI 每次拉 DeepSeek 权重都在赌 GFW 与 Hugging Face 不出事 —— hflock 用一个锁文件把权重的镜像、哈希、可重建性钉死在 Git 里。</b></p>

<h2><img src="https://api.iconify.design/tabler:topology-star-3.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 架构</h2>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./assets/atlas-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="./assets/atlas-light.svg">
  <img src="./assets/atlas-light.svg" width="880" alt="架构：lockfile → verify（HF 下载 + SHA256）→ manifest；m2 镜像目标待接入">
</picture>

<p>v0.1（m1）只打通蓝色与紫色这一段：<b>锁文件 → 从 Hugging Face 下载 → 重算 SHA256 → 输出 <code>weights.lock.manifest.json</code></b>。绿色清单就是给 CI 气隙门禁可校验的溯源记录。m2 才把权重镜像到 Gitee AI + ModelScope（图中虚线节点）。</p>

## 目录

- [为什么是 hflock](#为什么是-hflock)
- [安装与快速开始](#安装与快速开始)
- [用法](#用法)
- [演示](#演示)
- [配置](#配置)
- [对比](#对比)
- [路线图](#路线图)
- [付费 · Hosted tier](#付费--hosted-tier)
- [许可证](#许可证)

## 为什么是 hflock

国内 AI/DevOps 工程师把生产构建钉在 DeepSeek、Qwen、GLM 权重上，权威源只有一个 Hugging Face —— 在 GFW 内要么走一个**没有 SLA、没有溯源**的反向代理（hf-mirror.com），要么用 ModelScope 镜像一个**滞后且不完整**的子集。两条路都没法向审计或 CI 证明「这些字节是对的、且可重建」。

hflock 把这件事变成一个声明式锁文件：你提交 <code>weights.lock.yaml</code> 钉住 repo@revision + 文件，<code>hflock verify</code> 从 Hugging Face 下载、重算 SHA256、写出可机器校验的清单。在没有镜像之前，溯源链就已经可查 —— 这正是 m1 要先打通的原因。

<h2><img src="https://api.iconify.design/tabler:rocket.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 安装与快速开始</h2>

单二进制，无 Python 运行时。冷克隆到第一个可见结果，三条命令，离线（用内置 fixture 模拟 Hugging Face）：

```bash
git clone https://github.com/SuperMarioYL/hflock && cd hflock
go build -o hflock ./cmd/hflock
python3 -m http.server 8053 --directory examples/fixture & \
  HFLOCK_HF_BASE=http://localhost:8053 ./hflock verify examples/weights.lock.yaml
```

<details><summary>示例输出（weights.lock.manifest.json）</summary>

```json
{
  "lock_version": "0.1.0",
  "generated_at": "2026-08-27T16:36:17Z",
  "entries": [
    { "repo": "deepseek-ai/DeepSeek-V3", "revision": "v3.0", "file": "config.json", "sha256": "b6c28b2f…", "size": 146 },
    { "repo": "Qwen/Qwen3-235B-A22B", "revision": "main", "file": "tokenizer.json", "sha256": "8a9ebf95…", "size": 118 }
  ]
}
```
</details>

> 对真实 Hugging Face 跑（下载真权重大文件）：`go install github.com/SuperMarioYL/hflock@latest`，去掉 `HFLOCK_HF_BASE`，直接 `hflock verify weights.lock.yaml`。

<h2><img src="https://api.iconify.design/tabler:terminal-2.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 用法</h2>

写一个 `weights.lock.yaml` 钉住你 CI 依赖的国产权重：

```yaml
version: "0.1.0"
weights:
  - repo: deepseek-ai/DeepSeek-V3
    revision: v3.0
    files: ["config.json", "*.safetensors"]   # glob 会在校验时经 HF 文件树 API 展开
    source: huggingface
```

校验并生成溯源清单（m1 核心）：

```bash
hflock verify weights.lock.yaml                       # → weights.lock.manifest.json
hflock verify weights.lock.yaml -m ci/manifest.json   # 指定输出路径
hflock verify weights.lock.yaml --progress            # 大文件显示进度条
```

CI 气隙门禁：把清单提交进仓库，下次 `hflock verify` 重算哈希与清单比对即可判成败（m3 的 `--check` 会给标准退出码）。

完整示例见 [`examples/weights.lock.yaml`](./examples/weights.lock.yaml)。

<h2><img src="https://api.iconify.design/tabler:photo.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 演示</h2>

<p>离线 fixture 下，从锁文件到 SHA256 溯源清单的完整流程：</p>

<p align="center"><img src="./assets/demo.gif" width="720" alt="hflock demo"></p>

<p align="center"><sub>完整终端录制：<a href="./assets/demo.cast">asciinema cast</a> · 脚本：<a href="./docs/demo.tape">docs/demo.tape</a></sub></p>

<h2><img src="https://api.iconify.design/tabler:adjustments.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 配置</h2>

| 配置项 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `--hf-base` / `HFLOCK_HF_BASE` | string | `https://huggingface.co` | Hugging Face 源地址；离线 fixture / 自建镜像可覆盖 |
| `--workdir` / `HFLOCK_WORKDIR` | path | 系统 temp | 下载缓存目录 |
| `--manifest` / `-m` | path | `weights.lock.manifest.json` | 清单输出路径 |
| `--progress` | bool | `false` | 显示每文件下载进度条 |

## 对比

| 维度 | hflock | hf-mirror.com | ModelScope | huggingface-cli |
| --- | :---: | :---: | :---: | :---: |
| 声明式 pin（锁文件入 Git） | ✓ | ✗ | ✗ | ✗ |
| 多镜像（Gitee + ModelScope 并发） | ✓ (m2) | ✗（单反代） | ✗（单平台） | ✗ |
| 哈希溯源清单（CI 可校验） | ✓ | ✗ | ✗ | 部分（需手写 sha256sum） |
| 托管稳定性 / SLA | ✗（OSS 自托管） | ✗（社区代理） | ✓（阿里托管） | — |
| 零配置即用 | ✗（需写锁文件） | ✓ | ✓ | ✓ |

诚实说：要 SLA 与零配置，ModelScope / hf-mirror 现在就更好；hflock 卖点是**声明式 pin + 可校验溯源**这一块没人做。

<h2><img src="https://api.iconify.design/tabler:map-2.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 路线图</h2>

- [x] **m1 — 锁文件 + 哈希校验**：`WeightLock` 解析校验 + SHA256 验证，`hflock verify` 产出溯源清单（本版本）
- [ ] **m2 — 镜像同步**：`hflock sync` 读锁文件，从 Hugging Face 下载，并发上传到 Gitee AI + ModelScope，再跑 verify
- [ ] **m3 — init + CI 门禁**：`hflock init` 由 HF repo 生成锁文件；`hflock list` 逐权重状态表；`--check` 退出码做 CI 气隙门禁；完整 demo tape
- [ ] 未来：增量同步、sigstore 签名溯源、百度网盘/阿里云 OSS 镜像

## 付费 · Hosted tier

v0.1 只发 OSS 引擎（MIT），不卖任何东西。但商业路径是真实的，记在这里免得你问：

- **谁会付费**：受监管行业（金融/政务）的 CN ML 平台与合规岗，需要一份审计员能接受的「数据不出境」证明报告。
- **买什么**：托管镜像 + SLA + 签名（sigstore）溯源报告 —— 即 v0.1 OSS 引擎验证过的那套，叠加运营层。
- **价位区间**：约 ¥3k–8k/月/团队（低于一名 SRE 自托管的成本）。
- **最小闭环**：OSS CLI 在合规团队落地 → v0.2 在阿里云（ECS + OSS 存权重）开托管层 + 计费 → 每个 pinned 权重组一份签名溯源 PDF。
- **诚实门槛**：v0.1 达到 day-30 指标（≥100 star、≥10 活跃用户）前，不开付费层 —— 没有开源引流的商业层只是一张发票。

<h2><img src="https://api.iconify.design/tabler:license.svg?color=%230071E3&width=24" height="22" align="absmiddle" alt=""> 许可证</h2>

MIT，见 [LICENSE](./LICENSE)。提 issue 或 PR 都欢迎 —— 尤其欢迎补 Gitee AI / ModelScope 上传实现（m2）。

<p align="center"><sub><a href="./LICENSE">MIT</a> © 2026 SuperMarioYL</sub></p>
