[English](./README.en.md) · [Website](https://hflock.lei6393.com) · [GitHub](https://github.com/SuperMarioYL/hflock)

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/hero-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/hero-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/hero-dark.svg">
  <img src="./assets/presentation/hero-light.svg" width="960" alt="Hero diagram">
</picture>

# hflock

**为下载的模型文件生成可检查的清单。**

hflock 读取 YAML 中的仓库、版本和文件列表，下载文件，再生成包含哈希与大小的 JSON 清单。

## 为什么需要它

如果没有记录版本和实际字节，模型下载很难复现。把需要的文件写入锁文件，再将哈希清单与构建一起保存。

- **声明输入** — 把仓库、版本和文件名保存在 YAML 中。
- **记录实际字节** — 每次下载生成 SHA256 摘要与字节数。
- **离线复现** — 随仓 HTTP fixture 可运行下载与哈希计算链路。

## 架构

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/architecture-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-dark.svg">
  <img src="./assets/presentation/architecture-light.svg" width="960" alt="Architecture diagram">
</picture>

锁文件加载器校验请求；Hugging Face 源解析文件名或 glob，将下载流同时写入本地缓存并计算 SHA256。验证器把仓库、版本、路径、大小和摘要写入清单；它不会自动将摘要与可信基线比较。

| 组件 | 职责 |
| --- | --- |
| `YAML lockfile` | internal/lockfile |
| `HF source` | internal/mirror/hf.go |
| `Download + SHA256` | internal/verify/verify.go |
| `JSON manifest` | Hash entries and sizes |

## 安装与快速上手

使用仓库清单指定的运行时版本构建，并在仓库根目录运行示例。

```bash
git clone https://github.com/SuperMarioYL/hflock.git
cd hflock
go build ./cmd/hflock
```

Python 示例在 loopback 提供 examples/fixture，运行真实 verify 命令并打印生成的哈希；退出时停止服务并删除自身临时缓存。

```bash
python3 examples/presentation-demo.py
```

## 实际运行示例

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/process-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/process-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/process-dark.svg">
  <img src="./assets/presentation/process-light.svg" width="960" alt="Process diagram">
</picture>

Nine fixture files produce nine SHA256 entries; no mirror upload is attempted.

```text
source: local fixture; uploaded files: 0
deepseek-ai/DeepSeek-V3 config.json 146 b6c28b2ff63b535e4c3f1bac57c7da944ca8c098fbfab51befbd67d8b1ce59c7
deepseek-ai/DeepSeek-V3 generation_config.json 72 6d9c0a68128352b855b24b93d26cc6cb682de94fbe3bd5476cc548d76fbde572
deepseek-ai/DeepSeek-V3 tokenizer.json 121 2d162ff88f4721aaab432337fe7684c99b524f7b9ed12a96bac105471a8cdec0
Qwen/Qwen3-235B-A22B config.json 136 477797cbaa2e203b12b887f4dd0d50837f1083fb8c96614c60dc0a68e341ba51
Qwen/Qwen3-235B-A22B generation_config.json 72 6bdf409035dea3029e9f60144e1456598aa47d837381c524b441708ccafcb798
Qwen/Qwen3-235B-A22B tokenizer.json 118 8a9ebf95154a697efaf420257b2b31d198c8e81b840012f2aa170b38c6771677
THUDM/glm-4-9b-chat config.json 133 1227daa941278aa954f4e005cb4eed01af781ed2af76e539021fabb2faf34129
THUDM/glm-4-9b-chat generation_config.json 72 e06ed109d9bfa5faf7f3fedc0cc895d1fde272744ec9df0ccb13286fb28ce8ab
THUDM/glm-4-9b-chat tokenizer.json 117 35f88f9c371cb02c189d4f02a433629e028fc0f4f18f7d420ead0fef653edd2e
hashed files: 9
```

完整命令与输出保存在 [docs/demo-results.json](./docs/demo-results.json). 输入和复现代码均随仓提供。

![已有终端录制](./assets/demo.gif)

保留已有录制供参考；上方文字示例给出当前可复现的操作。

## 用法

CLI 提供以下操作。示例之外的命令需要替换成你的文件路径或标识。

```bash
go run ./cmd/hflock verify examples/weights.lock.yaml --manifest weights.lock.manifest.json
# Use your own lockfile for actual downloads:
hflock verify weights.lock.yaml --progress
```

## 配置

--hf-base 或 HFLOCK_HF_BASE 指定 HTTP 源；--workdir 或 HFLOCK_WORKDIR 指定下载缓存；--manifest/-m 指定输出文件；--progress 显示进度。示例启动 loopback fixture 服务并使用临时缓存。

## 集成与职责分工

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/integrations-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-dark.svg">
  <img src="./assets/presentation/integrations-light.svg" width="960" alt="Integrations diagram">
</picture>

以下路径已有源码实现。按任务选择输入，并把生成的结果与项目一起保存。

| 路径 | 已实现职责 |
| --- | --- |
| YAML | Repository/revision/file selection |
| Hugging Face HTTP | Download source |
| Loopback fixture | Offline reproduction |
| JSON manifest | SHA256 and byte counts |

## 限制与后续方向

- 示例计算九个小型元数据 fixture 的哈希，不是生产权重，也不进行远程上传。
- init、sync 和 list 尚未实现；Gitee AI 与 ModelScope 镜像属于后续计划。
- 新计算的摘要记录收到的字节，不证明上游真实性，也不校验期望摘要。需要复现时应使用不可变版本。

镜像上传、自动生成锁文件、与已有可信清单比较是后续工作。

## 许可与贡献

许可见 [LICENSE](./LICENSE). 反馈问题时请提供最小输入、执行命令和实际输出。
