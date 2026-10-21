# deepseek-cpa：CLIProxyAPI 的 DeepSeek 原生插件

## Problem Statement

部署者在用 CLIProxyAPI 代理 DeepSeek 时撞到三类问题。

**第一类是会话直接不可用。** DeepSeek 的 thinking 模型在带 `tools` 的请求里要求历史工具调用带上可回放的 reasoning；对上游不认识的调用（例如切模型后留下的其他模型的调用），缺少可回放内容就返回 400。内置 `openai-compatibility` 通道走的是「客户端协议 → 内部表示 → 上游协议」的转换路径，任何中间层丢字段都会把这个约束从「降级」变成「中断」。Claude Code 这类 Anthropic 客户端尤其严重：它自己的协议里 reasoning 块和工具调用是绑在一起的，转换一次就可能错位。

**第二类是三套协议的 thinking 参数互不相通。** DeepSeek 在 `chat-completions` 用 `thinking.type` 加 `reasoning_effort`，在 `anthropic` 用 `reasoning.effort` 加 `output_config.effort`，在 `responses` 用 `reasoning.effort`。内置通道不认这些差异，客户端只能自己猜哪个字段会生效。

**第三类是运维负担外移。** 多把 key、凭证来源、用量统计都得在代理外面再搭一套。

## Solution

一个 CLIProxyAPI 原生插件，标识 `deepseek-cpa`。

客户端按自己习惯的协议访问，用 `deepseek-cpa/<上游模型名>` 指名要这个插件。插件把**原始客户端 body** 送进 DeepSeek 的同协议端点——`chat-completions` 与 `responses` 打到 `https://api.deepseek.com`，`anthropic` 打到 `https://api.deepseek.com/anthropic`——只在模型名上改写。唯一例外是 ADR-0006 的受限历史转述：当带工具的历史里某个调用没有可回放的 reasoning 时，把该调用与结果改写成带历史标注的普通消息，或给 chat 的 assistant 消息补一个空 `reasoning_content`；不伪造 reasoning、不关闭 thinking。协议之间不转换。

凭证交给宿主：插件声明凭证解析能力，管理页通过宿主的凭证写入回调增删 key，轮转、限流、用量记账都由宿主完成。插件不复制一份密钥池。

thinking 参数由客户端按它所用协议的字段写入，插件原样透传、不改写（见 ADR-0004）。

上游的 401、402、429 原样透出，让宿主调度器看见并切换凭证；流式开始后上游断流，插件按协议发终态错误事件，而不是补一个正常结束。

## User Stories

1. 作为 Claude Code 用户，我想要把 `ANTHROPIC_BASE_URL` 指向 CLIProxyAPI 并使用 `deepseek-cpa/deepseek-v4-pro`，这样我不用改客户端代码就能用 DeepSeek。
2. 作为 Claude Code 用户，我想要在执行工具调用的多轮 thinking 会话里不遇到 400，这样长任务不会中途断掉。
3. 作为 Claude Code 用户，我想要 thinking 的 reasoning effort 通过我熟悉的 `output_config.effort` 生效，这样我不需要知道 DeepSeek 的内部字段名。
4. 作为 OpenAI SDK 用户，我想要用 `reasoning_effort` 加 `thinking.type` 控制思考模式，这样我按官方文档写的代码能直接跑。
5. 作为 Responses API 用户，我想要用 `reasoning.effort` 控制思考强度，这样我不必为了用 DeepSeek 换一套调用代码。
6. 作为 Responses API 用户，我想要流式事件保持它原本的类型和顺序，这样我的增量渲染不会乱序或丢事件。
7. 作为 Responses API 用户，我想要流式失败时收到 `response.failed` 事件，这样我能区分「正常结束」和「上游断了」。
8. 作为 Anthropic 用户，我想要流式失败时收到 `error` 事件，这样我的客户端能按标准路径处理错误。
9. 作为 chat 用户，我想要流式失败时在 `[DONE]` 之前收到错误块，这样我不会把截断当成功。
10. 作为部署者，我想要所有 DeepSeek key 由宿主的凭证机制管理，这样我不需要在插件配置里再存一份明文密钥。
11. 作为部署者，我想要在 CLIProxyAPI 管理页里增删 DeepSeek key，这样日常运维不用登服务器改文件。
12. 作为部署者，我想要单个 key 额度耗尽时宿主自动切到下一把，这样我不必手工切换。
13. 作为部署者，我想要上游错误码原样透出，这样我在宿主日志里能看到真实的 429 或 401 而不是被包装过的错误。
14. 作为部署者，我想要插件注册不依赖网络，这样 DeepSeek 抖动时整个代理服务不会少掉一条通道。
15. 作为部署者，我想要模型列表在首次需要时才向上游取，这样我不必为了新增模型发新版本。
16. 作为部署者，我想要在管理页手动触发一次模型列表刷新，这样 DeepSeek 上新模型后我能立刻看到。
17. 作为部署者，我想要交付的模型列表以上游实际返回为准，这样我不会看到已经下线的模型。
18. 作为部署者，我想要上游不可达时退回内置默认列表，这样冷启动期间已经有合理的模型可选。
19. 作为部署者，我想要客户端只能通过 `deepseek-cpa/` 前缀访问本插件，这样它不会和内置 DeepSeek 通道或别的插件抢请求。
20. 作为部署者，我想要裸 `deepseek-*` 模型名继续落到宿主原有路径，这样我已有的配置不会被我装插件这件事改变行为。
21. 作为部署者，我想要 `deepseek-cpa/deepseek-v4-pro[1m]` 这类带后缀的模型名被正确裁掉前缀并原样送到上游，这样长上下文变体可用。
22. 作为部署者，我想要每个请求的用量被记成结构化日志，这样我能自己接统计而不用插件内置一个看板。
23. 作为部署者，我想要插件声明的能力面就是它真的实现的那些，这样我读代码时不会撞到恒等函数的空壳。
24. 作为维护者，我想要三套协议的差异集中在一处判定，这样新增第四套协议时我知道该动哪里。
25. 作为维护者，我想要上游收到的字节与客户端原始 body 逐字节一致（除模型名与 ADR-0006 的受限历史转述外），这样任何字段丢失都能被测试抓住。
26. 作为维护者，我想要能力面的逻辑在 Go 层可直接测试，不经过 cgo 与 JSON envelope，这样大多数测试跑得快。
27. 作为维护者，我想要 ABI 握手有一层薄冒烟测试，这样 cgo 导出符号断掉时我能立刻知道。
28. 作为维护者，我想要依赖版本钉死到具体的 CLIProxyAPI 版本，这样上游改格式 token 或 schema 版本时我是被编译期或启动期拦住，而不是静默接不到请求。
29. 作为隐私敏感用户，我想要插件日志里不出现 key、`StorageJSON` 或原始请求体，这样日志可以放心留存。
30. 作为新用户，我想要插件在注册失败或配置非法时报出明确原因，这样我不用猜是版本不匹配还是字段写错。

## Implementation Decisions

**插件标识与命名空间。** 插件 ID 为 `deepseek-cpa`，与动态库文件名、`plugins.configs.deepseek-cpa` 配置键、模型命名空间前缀三者一致。客户端模型名必须是 `deepseek-cpa/<上游模型名>`；裁掉前缀后的部分原样作为上游模型名，包括 `[1m]` 这类后缀。不带前缀的模型名一律不由本插件处理，交回宿主的正常路径。

**协议面对齐。** 插件声明 `executor` 且同时声明三个协议的输入与输出格式：`chat-completions`、`responses`、`anthropic`。出站端点由入站协议决定，无额外配置项。这是 ADR-0001 的结论。发送前唯一允许的重写是 ADR-0006 的受限历史转述，它只按可观察结构触发，不识别来源模型。

**不声明恒等能力面。** `request_translator`、`response_translator`、`request_normalizer`、`response_normalizer`、`request_interceptor`、`response_interceptor`、`response_stream_interceptor`、`model_router`、`command_line_plugin` 全部不声明。声明五个真实能力面：`auth_provider`、`model_provider`、`executor`、`usage_plugin`、`management_api`。

**凭证归属。** 实现 `auth.identifier` 与 `auth.parse`。`auth.login.start`、`auth.login.poll`、`auth.refresh` 显式返回不支持——DeepSeek key 不过期，这三者对它是空活。凭证由管理页经宿主的凭证写入回调产生。这是 ADR-0002 的结论。

**模型公布。** 以 `model.static` 公布，配合 `executor_model_scope: static`。上游名与客户端可见名之间的差异只由前缀承载。

- 插件元数据注册（`plugin.register`）不联网，只校验配置。
- `model.static` 是列出模型的地方：没有任何凭证时零网络、返回内置默认列表；有凭证时探测上游 `/models` 并缓存结果。探测失败只记录日志并退回内置默认，绝不使注册或请求失败。
- 探测结果是列表真相。上游没有公布的模型不公布，因而**也不可路由**——宿主只路由已注册的模型。这条代价是真实的：`deepseek-v4-pro[1m]` 实测可用但上游不公布，探测成功后该名字会返回 400 `unknown provider`。这是刻意的取舍，不是缺陷。
- 管理路由可显式重新探测并更新插件侧列表；宿主在下一次插件重载（重启或改插件配置）后才应用新列表，宿主没有提供主动触发模型重注册的回调。

**thinking 不做插件侧写入。** 不实现 `thinking_applier`。宿主查找 applier 用的是协议格式名（`claude` / `openai` / `codex`），而插件的 applier 以 `thinking.identifier` 注册，两者永不相等，因此本插件的 applier 在这版宿主上不可能被调用；改用原生格式名注册也会被宿主以「与原生 applier 冲突」拒绝。同时宿主不对 `UserDefined` 的插件模型剥离 thinking 字段，客户端的 thinking 参数本来就能原样到达上游。结论：透传已经足够，多写一个不可达的能力面只会增加误导。这是 ADR-0004 的结论，取代 ADR-0003。

**错误语义。** 上游的 401、402、429 与其它状态码原样作为执行结果透出，executor 内部不重试、不改写状态码，让宿主调度器据此切换凭证。仅在需要携带宿主可识别的错误信息时才附加错误体，不吞掉原始状态。

**流式处理。** 谁给 chunk 作帧取决于协议面，三者不同：

- `responses` 与 `anthropic`：宿主把执行器交出的字节原样转发，所以 chunk 必须是完整 SSE 帧；插件合成终态错误事件时自己写 `event:` / `data:` 前缀。
- `chat-completions`：宿主自己写 `data: <payload>` 并自己补上 `[DONE]`，所以 chunk 必须是**裸 JSON 载荷**；插件把上游 SSE 拆成单个事件再交出，丢掉上游的 `[DONE]`，合成的失败载荷同样不带前缀也不带 `[DONE]`。把带前缀的整帧交给宿主会产生 `data: data:` 双重前缀。

流开始后上游异常终止时按协议发终态错误事件；未收到上游终态事件就结束的流，一律按失败处理，不得补发正常终态。终态按各协议自己的终止帧判断：responses 看 `response.completed` / `response.incomplete` / `response.failed`，anthropic 看 `message_stop` 与 `error`，chat 看 `[DONE]`；判定只读事件的类型字段，不做文本子串匹配，避免把正文里出现的同名文本当成流结束。

**用量观测。** 实现 `usage_plugin`，把宿主投递的用量记录落成结构化日志。不存储、不聚合、不提供看板。

**管理接口。** 实现 `management_api`，提供两个面：密钥的列出、新增、删除（新增与删除经宿主的凭证写入回调），以及模型列表的显式刷新。密钥相关操作走需要管理密钥的 `/v0/management` 路由，不放在无需鉴权的资源页里。

**依赖与版本。** `go.mod` 钉死具体的 CLIProxyAPI v7.x 版本，不使用浮动版本。插件注册时声明与所实现行为相符的 schema 版本。

**日志纪律。** 任何路径都不得记录 key、`StorageJSON`、原始请求体或响应体。

## Testing Decisions

**好测试的标准。** 只断言外部行为：给定客户端输入，断言上游收到的字节与插件交给宿主的字节。不断言内部函数调用顺序、不 mock 插件自己的类型、不为覆盖率写测试。零转换直通这个特性本身就等价于「字节保真」，因此字节级断言不是实现细节，而是行为契约。

**缝 A（主缝）。** 在 Go 层通过插件构建入口拿到能力面接口，用 Go 结构体直接调用各方法。绝大多数测试落在这里：模型前缀裁切、协议到端点的映射、thinking 字段原样透传、错误状态码透传、流式终态错误事件、用量记录形状、能力面声明。

**缝 B（传输边界）。** 插件对上层的 HTTP 与凭证写入调用集中在一个窄接口后面，测试注入假实现并记录插件交给宿主的 `HTTPRequest`。假上游落在这一层而不是真起 HTTP server：它比 server 更靠近契约，且 URL 是编译期常量。这里断言两类契约：非流式请求的上游 body 与客户端原始 body 除模型名（以及 ADR-0006 明确命中的局部替换）外逐字节一致；流式响应的帧序列逐条、逐字节与上游返回一致。历史转述另需断言未命中的 item/顶层字节不变、改写结果确定且幂等。三个协议面在这里用表驱动覆盖，不为每个协议单开一条测试路径。

**缝 C（薄层冒烟）。** 一到两个测试覆盖动态库初始化入口、envelope 的 `ok` / `error` 编解码、注册响应里的能力面声明字段。只验证握手没断，不重复 A、B 的断言。

**前序参照。** `cpa-plugin-commandcode` 的 `Build` 加直接调用能力面的测试形状是本项目最接近的先例。但它用的格式 token 与当前 CLIProxyAPI 不同（它用旧 token，宿主当前使用 `chat-completions` / `responses` / `anthropic`），因此只能借鉴测试组织方式，不能照抄常量。

## Out of Scope

- 跨协议翻译。Anthropic 客户端要打 `chat-completions` 端点这类交叉组合，继续走宿主内置转换器。
- 自管密钥池与插件内的凭证轮转。凭证与调度全部交给宿主。
- 用量看板、统计聚合、计费。只落日志。
- `model_router`。与宿主凭证选择互斥。
- 命令行扩展。
- 第三方中转或逆向上游。只支持 DeepSeek 官方端点。
- 裸 `deepseek-*` 模型名的接管。
- 温度、`presence_penalty`、`frequency_penalty` 在 thinking 模式下的清理或归一化。上游本来就静默忽略，插件不改写客户端可见行为。
- 伪造 reasoning、关闭 thinking、按模型来源分流历史。ADR-0006 的转述只按可观察结构触发，不识别来源模型。
- 插件商店发布所需的打包与发布流水线（可在实现阶段作为独立工单处理）。

## Further Notes

**三条需在实现时验证的事实，不得假定：**

1. 静态模型路径下宿主是否仍会选中凭证记录。`model_router` 文档中「不选凭证」的表述只对 router-self 路由成立，normal 路径未经证实。若不选，`model.static` 与 `executor_model_scope: static` 的组合需改为凭证绑定模型。
2. 当前钉住的 CLIProxyAPI 版本的格式匹配器是否接受旧 token。这决定 `cpa-plugin-commandcode` 的常量能否作为参考。
3. schema 版本该声明几。从 3 起 payload 流分块不再携带原始请求体，本插件逐 event 原样上交，理论上应声明最高版本，但需确认宿主对高版本的兼容承诺。

**部署前验证。** ADR-0006 的转述发生在宿主把 `Payload` 交给执行器之后；宿主对 websocket 历史合并、`previous_response_id` 移除的全路径未在已部署版本上逐一验证。部署前需用合成请求做真实续轮 smoke。

**仓库状态。** 当前目录此前不是 git 仓库，spec 发布前需先初始化并关联远端仓库。
