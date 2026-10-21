# deepseek-cpa

CLIProxyAPI 的 DeepSeek 原生插件。客户端按自己习惯的协议访问，插件把请求送进 DeepSeek 同协议端点，不做跨协议翻译。

## Language

**协议面**：
DeepSeek 对外提供的三种入站协议之一：`chat-completions`、`anthropic`、`responses`。一个插件实例同时服务三个协议面。
_Avoid_: 格式、端点、API 类型

**协议对齐直通**：
客户端用哪个协议面发请求，插件就把请求原样送进 DeepSeek 的同一个协议面。插件不在协议面之间转换，只在必要处改写模型名。ADR-0006 的历史工具调用转述是唯一允许的发送前重写例外，其余字段不动。
_Avoid_: 翻译、适配、转换层

**上游端点**：
承载某个协议面的 DeepSeek 服务地址。`chat-completions` 与 `responses` 在 `https://api.deepseek.com`，`anthropic` 在 `https://api.deepseek.com/anthropic`。
_Avoid_: 后端、通道

**reasoning 回放**：
`responses` 协议面上，带 `tools` 的 thinking 会话要求历史工具调用在同一 assistant 输出段内带可回放的 `reasoning_text`；`chat-completions` 对应字段是 `reasoning_content`。实测 chat 面上空字符串也被接受，缺字段才会 400；responses 上缺 reasoning item、空 `content` 或空 `text` 都会 400，但这是对上游不认识的调用实测的结果——DeepSeek 自己签发过的调用即使删掉 reasoning 仍返回 200，所以「缺块」是重写判据，不是等价于上游必然拒绝。这是协议对齐直通要保护的首要事实。实测 `anthropic` 协议面不施加这条约束，所以「没有 400」不能当作保真的证据，保真只能由字节比对证明。
_Avoid_: 上下文恢复、思维链传递

**历史转述**：
ADR-0006 允许的唯一发送前重写：把没有可回放 reasoning 的工具调用与结果改成带 `[Historical …]` 标签的普通 assistant/user 消息。它不识别来源模型，也不声称语义无损。
_Avoid_: 伪造 reasoning、清理历史

**凭证**：
宿主管理的一条 DeepSeek API key 记录。`StorageJSON` 由插件拥有，插件不自己维护密钥池。
_Avoid_: 密钥、key、账号

**模型命名空间**：
客户端可见的 `deepseek-cpa/` 前缀。前缀之后的部分原样作为上游模型名，包括 `[1m]` 这类后缀。
_Avoid_: 别名、模型组

**能力面**：
插件向宿主声明的可调用接口，例如 `executor`、`auth_provider`。声明了就必须真的干活。
_Avoid_: 能力、模块、功能

**懒探测**：
首次需要模型列表时才向上游取并缓存。注册阶段不联网，因此上游不可达不影响插件加载。
_Avoid_: 预加载、启动拉取
