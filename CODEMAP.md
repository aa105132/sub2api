# 代码地图

> 用途：为后续修改提供稳定入口，减少每次都从零开始找代码。  
> 规则：开始任务先看这里；改完代码后，把本次涉及的位置继续补进来。

## 使用方式

- 先按“目录分组”找到大概模块。
- 再按“入口 / 关键点”跳到具体文件。
- 如果本次新发现了稳定入口，任务结束前补充到对应分组。

## 根目录

- `backend/`：后端主服务，网关、鉴权、调度、外部接口都在这里。
- `frontend/`：前端页面与管理界面。
- `docs/`：补充文档。
- `deploy/`：部署相关资源。
- `tools/`：辅助脚本与工具。
- `AGENTS.md`：仓库级 Agent 规则，要求先读代码地图、改完回写地图。
- `CODEMAP.md`：当前代码地图文件。

## Backend 启动与装配

- `backend/cmd/server/wire.go`
  - 入口：Wire 依赖装配入口。
  - 作用：声明服务启动需要的 provider 组合。
  - 关联：`backend/cmd/server/wire_gen.go`、`backend/internal/service/wire.go`、`backend/internal/handler/wire.go`

- `backend/cmd/server/wire_gen.go`
  - 入口：Wire 生成后的实际注入代码。
  - 作用：启动时把 config / repo / service / handler / router 串起来。
  - 改这里前，先确认对应 provider 是否已经在 `wire.go` 中声明。

## 路由总入口

- `backend/internal/server/router.go`
  - 入口：`SetupRouter`、`registerRoutes`
  - 作用：注册全局中间件与所有模块路由。
  - 关键点：`RegisterGatewayRoutes`、`RegisterExternalCodexRoutes`
  - 关联：`backend/internal/server/routes/*.go`

- `backend/internal/server/routes/gateway.go`
  - 入口：Gateway 路由注册
  - 作用：挂载 `/v1/messages`、`/v1/chat/completions`、OpenAI/Anthropic 分流逻辑。
  - 本次地图重点：非 OpenAI 分组的 `/v1/chat/completions` 兼容入口也在这里。
  - 关联：`backend/internal/handler/gateway_chat_completions_compat.go`

- `backend/internal/server/routes/external_codex.go`
  - 入口：`RegisterExternalCodexRoutes`
  - 作用：暴露外部 Codex 凭证接口。
  - 路由前缀：
    - `/api/external/codex`
    - `/api/v1/external/codex`
  - 关键接口：
    - `GET /status`
    - `GET /auth-url`
    - `POST /callback`
    - `POST /direct-push`
    - `POST /status`
    - `POST /team/vacancies`
    - `POST /team/info`
    - `POST /team/invite`
    - `POST /team/kick`
    - `POST /team/cleanup`

## Handler 层

- `backend/internal/handler/handler.go`
  - 入口：`Handlers`
  - 作用：所有 HTTP handler 聚合点。
  - 本次地图重点：新增 `ExternalCodex *ExternalCodexHandler`

- `backend/internal/handler/wire.go`
  - 入口：handler provider 装配
  - 作用：把 service 注入到各个 handler。
  - 改 handler 后常常要同步这里。

- `backend/internal/handler/gateway_chat_completions_compat.go`
  - 入口：`GatewayHandler.ChatCompletions`
  - 作用：给非 OpenAI 分组提供 `/v1/chat/completions` 兼容层。
  - 关键流程：
    - Chat Completions 请求转 Anthropic Messages
    - 复用通用 `Messages` 链路做调度 / 缓存 / 计费
    - 再把响应转回 Chat Completions
  - 关联：
    - `backend/internal/pkg/apicompat/chatcompletions_to_anthropic.go`
    - `backend/internal/pkg/apicompat/anthropic_to_chatcompletions.go`
    - `backend/internal/server/routes/gateway.go`

- `backend/internal/handler/external_codex_handler.go`
  - 入口：`ExternalCodexHandler`
  - 作用：对外暴露 Codex 凭证管理与 Team 管理接口。
  - 关键点：
    - 成功直接返回原始 JSON
    - 失败统一返回 `{"detail":"..."}`
    - 会从 body / header / query 兜底取 `api_key`、`admin_password`
    - `bindExternalCodexJSON` 已兼容 `snake_case` / `camelCase` 外部请求体，便于对接 `CatieCli`
  - 关联：`backend/internal/service/codex_external_service.go`

## Service 层

- `backend/internal/service/wire.go`
  - 入口：service provider 集合
  - 作用：声明各 service 的构造依赖。
  - 本次地图重点：已接入 `NewOpenAIChatGPTTeamService`、`NewCodexExternalService`

- `backend/internal/service/openai_gateway_service.go`
  - 入口：OpenAI 网关公共能力
  - 作用：统一处理上游请求、错误透传、failover、副作用、账号状态维护。
  - 关键点：这里有多处 `handleCodexCredentialFailure(...)` 调用。
  - 关联：
    - `backend/internal/service/openai_gateway_chat_completions.go`
    - `backend/internal/service/openai_gateway_messages.go`
    - `backend/internal/service/openai_gateway_codex_cleanup.go`

- `backend/internal/service/openai_gateway_chat_completions.go`
  - 入口：`OpenAIGatewayService.ForwardAsChatCompletions`
  - 作用：处理 OpenAI OAuth 账号下的 `/v1/chat/completions` 请求。
  - 本次地图重点：Codex OAuth 模型在这里会做请求转换，并在 `401/402` 时触发自动清理。

- `backend/internal/service/openai_gateway_messages.go`
  - 入口：`OpenAIGatewayService.ForwardAsAnthropic`
  - 作用：处理 OpenAI OAuth 账号下的 `/v1/messages` 请求。
  - 本次地图重点：Codex OAuth 模型在这里会做消息格式兼容，并在 `401/402` 时触发自动清理。

- `backend/internal/service/openai_gateway_codex_cleanup.go`
  - 入口：
    - `shouldAutoDeleteCodexCredential`
    - `handleCodexCredentialFailure`
    - `resolveCodexTeamOwnerAccount`
  - 作用：当 OpenAI OAuth 类型的 Codex 凭证遇到上游 `401/402` 时，自动删除本地凭证。
  - 关键副作用：
    - 先 `SetSchedulable(false)`
    - Team 成员账号 best-effort 踢出团队
    - 再删除本地账号

- `backend/internal/service/openai_chatgpt_team_service.go`
  - 入口：
    - `GetTeamSnapshot`
    - `InviteMembers`
    - `RemoveMemberOrInvite`
  - 作用：封装 ChatGPT Team 相关操作。
  - 适用场景：查看 Team 信息、邀请成员、删除成员 / 邀请记录、清理失效成员。

- `backend/internal/service/codex_external_service.go`
  - 入口：
    - `PublicStatus`
    - `GenerateAuthURL`
    - `Callback`
    - `DirectPush`
    - `Status`
    - `TeamVacancies`
    - `TeamInfo`
    - `TeamInvite`
    - `TeamKick`
    - `TeamCleanup`
  - 作用：对外暴露 Codex 凭证管理、凭证推送、Team 查询与清理能力。
  - 关键点：
    - 兼容外部 Codex API 调用
    - 本地凭证按 `PlatformOpenAI + AccountTypeOAuth` 落库
    - 依赖 Team 服务做成员管理

## 协议兼容层

- `backend/internal/pkg/apicompat/chatcompletions_to_anthropic.go`
  - 入口：
    - `ChatCompletionsToAnthropic`
    - `InjectAnthropicCompatSessionMetadata`
  - 作用：把 Chat Completions 请求转换成 Anthropic Messages 请求，并补 session / cache 元数据。

- `backend/internal/pkg/apicompat/anthropic_to_chatcompletions.go`
  - 入口：
    - `NewAnthropicToChatState`
    - `FinalizeAnthropicChatStream`
  - 作用：把 Anthropic SSE / 消息响应转换回 Chat Completions 格式。

## 配置

- `backend/internal/config/config.go`
  - 入口：全局配置定义与默认值
  - 本次地图重点：
    - `CodexExternalAPIKey`
    - 默认值 `default.codex_external_api_key`
  - 改外部 Codex 接口鉴权时，先看这里。

## 测试

- `backend/internal/service/openai_gateway_compat_codex_test.go`
  - 作用：验证 Codex OAuth 在 `/v1/chat/completions` 与 `/v1/messages` 下的格式转换、session / cache 透传是否正确。
  - 适用场景：改兼容层、改请求转换、改响应转换时优先回归这里。

- `backend/internal/handler/external_codex_handler_test.go`
  - 作用：验证 external codex 接口能正确解析 `snake_case` 请求体。
  - 本次改动：覆盖 `direct-push`、`team/info` 对 `api_key`、`access_token`、`owner_credential_id`、`include_members` 等字段的兼容绑定。
  - 关联：`backend/internal/handler/external_codex_handler.go`

## 最近补充

- `AGENTS.md`
  - 本次改动：新增仓库级 Agent 规则，要求“先读代码地图，再改代码；改完同步回写代码地图”。
  - 关联：`CODEMAP.md`

- `CODEMAP.md`
  - 本次改动：初始化仓库代码地图，先覆盖启动装配、路由、Codex 外部接口、兼容层、OpenAI 网关与相关测试。
  - 后续维护：继续按模块增量追加，不需要一次写完整个仓库。

- `backend/internal/service/setting_custom_endpoint_models.go`
  - 入口：`GetCustomEndpointModelSettings`、`GetCustomEndpointModelsForAccount`
  - 作用：维护 `custom_endpoint_models` 设置项的解析、归一化、按 `platform + base_url` 匹配。
  - 本次改动：新增自定义端点模型设置的数据结构、URL 规范化与匹配逻辑。
  - 关联：`backend/internal/service/setting_service.go`、`backend/internal/handler/admin/setting_handler.go`

- `backend/internal/service/setting_service.go`
  - 入口：`UpdateSettings`、`InitializeDefaultSettings`、`parseSettings`
  - 作用：系统设置统一读写入口。
  - 本次改动：把 `custom_endpoint_models` 纳入默认值、后台保存、后台读取。
  - 关联：`backend/internal/service/settings_view.go`、`backend/internal/handler/dto/settings.go`

- `backend/internal/handler/admin/setting_handler.go`
  - 入口：`GetSettings`、`UpdateSettings`
  - 作用：管理员系统设置接口。
  - 本次改动：支持返回 / 保存 `custom_endpoint_models`，并增加 DTO 与 service 互转 helper。
  - 关联：`backend/internal/service/setting_custom_endpoint_models.go`、`frontend/src/views/admin/SettingsView.vue`

- `backend/internal/handler/admin/account_handler.go`
  - 入口：`GetAvailableModels`
  - 作用：管理员侧账号模型列表接口。
  - 本次改动：当账号未配置 `model_mapping` 时，优先按账号 `platform + base_url` 读取自定义端点模型并生成平台对应的模型列表对象。
  - 关联：`backend/internal/service/setting_custom_endpoint_models.go`、`backend/cmd/server/wire_gen.go`

- `backend/internal/service/gateway_service.go`
  - 入口：`GetAvailableModels`
  - 作用：网关 `/v1/models` 的聚合模型列表来源。
  - 本次改动：除 `model_mapping` 外，也会聚合匹配到的自定义端点模型，并进入短 TTL 缓存。
  - 关联：`backend/internal/service/gateway_hotpath_optimization_test.go`

- `frontend/src/views/admin/SettingsView.vue`
  - 入口：`loadSettings`、`saveSettings`
  - 作用：管理员系统设置页。
  - 本次改动：把“自定义端点模型”从 JSON 文本框改成可视化表单，支持行级增删/上下移动、平台选择、`base_url` 输入和模型标签录入。
  - 关联：`frontend/src/api/admin/settings.ts`、`frontend/src/stores/adminSettings.ts`

- `frontend/src/stores/adminSettings.ts`
  - 入口：`useAdminSettingsStore`
  - 作用：缓存管理员设置，供导航 / 模型选择器等前端组件复用。
  - 本次改动：缓存 `custom_endpoint_models`，供模型白名单选择器动态读取。
  - 关联：`frontend/src/components/account/ModelWhitelistSelector.vue`

- `frontend/src/components/account/ModelWhitelistSelector.vue`
  - 入口：模型白名单多选组件
  - 作用：账号创建 / 编辑 / 批量编辑时选择允许模型。
  - 本次改动：支持传入 `baseUrl` / `baseUrls`，会把匹配 `platform + base_url` 的自定义端点模型合并进候选列表。
  - 关联：`frontend/src/components/account/CreateAccountModal.vue`、`frontend/src/components/account/EditAccountModal.vue`、`frontend/src/components/account/BulkEditAccountModal.vue`
