# cpa-plugin-deepseek

CLIProxyAPI 的 DeepSeek 原生插件。客户端用它自己的协议访问，插件把请求原样送进 DeepSeek 的同协议端点——**不做跨协议翻译**。

插件标识：`deepseek-cpa`（同时是插件 ID、`.so`/`.dylib` 文件名、配置键、模型命名空间前缀）。

## 它解决什么

内置的 `openai-compatibility` 通道能跑 DeepSeek，但走的是「客户端协议 → 内部表示 → 上游协议」的转换路径。DeepSeek 有三个协议面，各自的 thinking 字段和分帧规则都不同，任何中间层丢字段都会让会话从「降级」变成「中断」。

本插件只做三件事：判定协议面 → 选对应端点 → 改写模型名。其余字节原样转发；唯一例外是历史工具调用缺少可回放 thinking 时的发送前转述（见 [ADR-0006](docs/adr/0006-narrate-unreplayable-tool-history.md)）。详见 [ADR-0001](docs/adr/0001-protocol-aligned-passthrough.md)。

| 客户端协议 | 上游端点 |
|---|---|
| `anthropic`（Claude Code 等） | `https://api.deepseek.com/anthropic/v1/messages` |
| `chat-completions`（OpenAI SDK 等） | `https://api.deepseek.com/chat/completions` |
| `responses` | `https://api.deepseek.com/responses` |

## 要求

- CLIProxyAPI v8.0.32（以 `go.mod` 钉住的版本为准），且构建时启用了 cgo 插件支持（`X-CPA-SUPPORT-PLUGIN: 1`）
- 平台：`linux/{amd64,arm64}`、`darwin/{amd64,arm64}`、`windows/{amd64,arm64}`、`freebsd/amd64`

  与 CLIProxyAPI 官方发布的可加载插件的平台清单一致。`freebsd/arm64` 不在其中：上游只提供 `freebsd_aarch64_no-plugin` 构建，没有能加载插件的宿主。

## 安装

从 [Releases](https://github.com/xspeed1989/cpa-plugin-deepseek/releases) 下载对应平台的 zip，解压到 CLIProxyAPI 的 `plugins/` 目录：

```bash
# 以 linux/amd64 为例，其他平台把文件名里的平台段换掉即可
version=0.3.0
curl -sL -O "https://github.com/xspeed1989/cpa-plugin-deepseek/releases/download/v${version}/deepseek-cpa_${version}_linux_amd64.zip"
unzip -o "deepseek-cpa_${version}_linux_amd64.zip" -d <cliproxyapi>/plugins/
```

解压后目录里应是 `deepseek-cpa.so`（macOS 为 `deepseek-cpa.dylib`）——插件 ID 就取自这个文件名，不能改。

在 `config.yaml` 里启用：

```yaml
plugins:
  enabled: true
  configs:
    deepseek-cpa:
      enabled: true
```

插件**没有自己的配置字段**：端点固定为 DeepSeek 官方地址，模型列表来自上游探测。

启动后确认注册成功：

```bash
curl -s -H "X-Management-Key: <你的管理密钥>" \
  http://127.0.0.1:8317/v0/management/plugins | jq '.plugins[] | {id, registered, effective_enabled}'
```

## 管理 DeepSeek 密钥

密钥由宿主托管。两种方式：

### 管理面板（推荐）

管理面板 → 插件 → **DeepSeek Keys**（`/v0/resource/plugins/deepseek-cpa/credentials`）。

面板登录时勾选过「记住密码」的话，页面会自动读取并解码面板保存的那把管理密钥，不需要再填；否则才提示输入（只在本机浏览器保存一次）。之后可以直接查看、添加、删除密钥。

### 命令行

以下路由都需要管理密钥。

```bash
# 新增（同一个 key 重复添加会覆盖同名凭证）
curl -s -X POST -H "X-Management-Key: <key>" -H "Content-Type: application/json" \
  -d '{"api_key":"sk-..."}' \
  http://127.0.0.1:8317/v0/management/plugins/deepseek-cpa/keys

# 列出（不会返回密钥本身）
curl -s -H "X-Management-Key: <key>" \
  http://127.0.0.1:8317/v0/management/plugins/deepseek-cpa/keys

# 删除
curl -s -X DELETE -H "X-Management-Key: <key>" \
  "http://127.0.0.1:8317/v0/management/plugins/deepseek-cpa/keys?name=deepseek-cpa-<hash>.json"
```

凭证文件落在宿主的 auth 目录（`oauth.auth-dir`），内容是 `{"type":"deepseek-cpa","api_key":"..."}`。删除是幂等的：文件已不存在也返回 204。

## 使用模型

客户端必须带 `deepseek-cpa/` 前缀，前缀之后的部分原样作为上游模型名：

```bash
# OpenAI SDK
client = OpenAI(base_url="http://127.0.0.1:8317/v1", api_key="<代理的客户端密钥>")
client.chat.completions.create(model="deepseek-cpa/deepseek-v4-pro", messages=[...])

# Claude Code
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=<代理的客户端密钥>
export ANTHROPIC_MODEL=deepseek-cpa/deepseek-v4-pro
```

**模型列表以上游为准。** 插件向 `https://api.deepseek.com/models` 探测并发布结果；上游没公布的模型不会被注册，因而也无法路由。每条模型的能力参数（上下文窗口、输出上限、输入模态、推理档位）同样照上游发布，插件不自带默认值：留空的话宿主会套用它自己的 Codex 默认值（272000 上下文、16384 输出、所有模型都收图像、默认 `medium` 推理），从而谎报 DeepSeek 的实际限制。

- 没有任何 DeepSeek 凭证时：返回内置引导列表，且不发起任何网络请求。
- 探测失败：退回内置列表并记一条日志，不影响插件加载与请求。
- 强制重新探测：`POST /v0/management/plugins/deepseek-cpa/models/refresh`。宿主要在下次插件重载（重启或改插件配置）后才应用新列表。

## 用量日志

每个完成的请求写一条结构化日志，JSON 在消息体内：

```
request usage {"model":"deepseek-cpa/deepseek-v4-pro","response_model":"deepseek-v4-pro",
"provider":"deepseek-cpa","stream":true,"failed":false,"status":200,
"input_tokens":84,"output_tokens":8,"reasoning_tokens":8,"cached_tokens":0,"total_tokens":92,
"latency_ms":1187,"ttft_ms":1134,"request_id":"…","trace_id":"…"}
```

日志不含密钥、凭证内容、请求体或响应体。插件不存储、不聚合、不提供看板——统计请自己接这条日志。

## 已知限制

- **只支持 DeepSeek 官方端点**。第三方中转或逆向上游不在范围内。
- **模型列表严格照上游**：上游不公布但实际可用的名字（例如 `deepseek-v4-pro[1m]`）在探测成功后不可用，因为宿主只路由已注册的模型。这是刻意取舍，见 [spec](docs/spec/0001-deepseek-cpa-plugin.md) 的模型公布段落。
- **历史工具调用缺 thinking 时会被转述**：切模型后历史里可能带着别的模型的工具调用。`responses` 面会把缺可回放 `reasoning_text` 的调用与结果改写成带历史标注的普通 assistant/user 消息；`chat-completions` 面只给缺字段的 assistant 消息补空 `reasoning_content`。不伪造 thinking、不关闭 thinking；原生但缺 reasoning 的调用也可能被这样处理。见 [ADR-0006](docs/adr/0006-narrate-unreplayable-tool-history.md)。
- **不实现 `thinking_applier`**：宿主的 applier 查找键是协议格式名，插件按 `thinking.identifier` 注册，两者永不相等，该能力面不可达。客户端的 thinking 参数靠透传生效。见 [ADR-0004](docs/adr/0004-no-thinking-applier.md)。
- **用量日志没有协议面**：宿主的用量记录没有格式字段，且其 `request_id` 与执行请求不相关，无法反查。
- **运行中新增凭证不会立刻刷新模型列表**：宿主只在插件重载时询问模型。

## 开发

```bash
./build.sh                                  # 当前平台的 .so/.dylib → dist/
go test ./...                               # 单测（不联网）
python3 test/abi_smoke.py dist/deepseek-cpa.linux.amd64   # 真实动态库的 ABI 冒烟
./scripts/package-release.sh 0.2.0          # 打 release 归档 + checksums
python3 test/package_check.py               # 校验归档与 registry.json
```

真机验证脚本需要一把真实 DeepSeek key（key 由宿主持有，脚本本身不接触）：

```bash
python3 test/anthropic_e2e.py   # 多轮 tools + thinking 会话
python3 test/stream_e2e.py      # anthropic 与 responses 的流式终止事件
python3 test/chat_e2e.py        # thinking 开关双向对照、usage、[DONE]
```

### 发布

打 tag 即发布，矩阵取自 [`platforms.txt`](platforms.txt)：

```bash
# 先把 registry.json 的 version 改成与 tag 一致，发布流程会校验
git tag v0.2.0 && git push origin v0.2.0
```

CI 在原生 runner 上分别构建（FreeBSD 在 VM 内构建），汇总 `checksums.txt` 并创建 Release。也可以在 Actions 里手动触发 `release` 工作流试跑整张矩阵——手动运行不会发布。`platforms.txt` 是平台矩阵的唯一来源，`build.sh`、`scripts/package-release.sh` 与发布工作流都经 `scripts/platforms.sh` 读取它。

## 文档

- [CONTEXT.md](CONTEXT.md) —— 术语表
- [docs/adr/](docs/adr/) —— 架构决策（ADR-0001…0006）
- [docs/spec/0001-deepseek-cpa-plugin.md](docs/spec/0001-deepseek-cpa-plugin.md) —— 完整规格
