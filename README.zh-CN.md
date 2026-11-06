# CPA_Quota_Sentinel

简体中文 | [English](README.md) | [更新日志](CHANGELOG.zh-CN.md)

`CPA_Quota_Sentinel` 是 CLIProxyAPI（CPA）插件，用于保护 Codex OAuth 账号额度。
额度偏低时暂停账号，重置后重新核实额度，仅自动恢复由插件暂停的账号。

## 多提供商账号

总览显示全部上游 AUTH 账号。Codex 使用原有刷新器；Claude、Antigravity 和 Grok Build (xAI OAuth) 通过上游只读接口获取真实额度。AGY 将普通 Gemini、Claude/GPT 与 Gemini 4 Argon 分别保护，不会因 Argon 0% 阻断其他模型；上游未返回的 Argon 配额保持未知，不推断该模型不可用。xAI 仅使用真实返回的周/月账单额度，不虚构 5 小时窗口。API Key 不视为 OAuth 订阅额度。非 Codex 账号在最高可用 CPA 优先级内轮换，本次尚未使用真实 xAI/AGY 凭据验证。

在 CPA 配置中修改 `management_path` 后，请重启 CPA 重新注册路由，再打开插件侧栏入口。CPA 的 hash 页面地址由宿主管理，不会随插件资源路径变化。在登录卡片输入 CPA 管理密钥加载数据。


## 本仓库新增功能

插件 ID 和动态库名为 `cpa-quota-sentinel`。

- 5 小时与周/月窗口分别配置剩余额度暂停阈值，默认均为 20%。
- 重置后重新查询额度，达到恢复阈值（默认 30%）才允许恢复。
- 支持没有 5 小时窗口的账号；已知窗口在恢复时缺失不会被当作无限额度。
- 持久化暂停记录、凭据指纹及插件所有权标记。
- 自定义管理路径、暂停状态展示、日志及策略 JSON 导出。
- 管理后台内置额度哨兵策略面板：统一暂停阈值滑块、可展开的 5 小时/周月独立阈值、恢复阈值滑块、检查间隔快捷选择、自动恢复开关及保存生效按钮。
- 可切换的「账号轮转队列」：**自动模式仍为默认**；自定义模式支持拖拽、置顶，读取 CPA 上游认证权重（0～100 万，未设置默认 1），并并列展示 Codex、Claude、Antigravity、xAI 等账号真实的 5 小时与周/月额度，按兼容模型候选集独立轮转。
- 英文为默认 README，提供简体中文文档和中英文管理界面。

**额度保护默认关闭，需在插件页面的“调度设置”中开启并保存。**
暂停阈值默认 20%，恢复阈值默认 30%，保护中账号每 5 分钟复核。
启用保护后，Codex 账号即使近期没有请求，也会按保护间隔刷新额度；Claude、Antigravity 和 xAI OAuth 同样使用该间隔。账号清单验证、认证退避等安全规则仍然生效。
正式安装包由 GitHub Actions 使用对应平台编译器构建；真实 CPA 部署与额度提供商的集成仍需安装后验证。

### 快速使用

1. 使用 Go 1.26 和 C 编译器构建本仓库。上游商店安装包不包含本仓库的新增功能。
2. 停止 CPA，将对应系统和架构的动态库放进 CPA 插件目录，再启动 CPA。
3. 在 CPA 配置中启用插件。
4. 访问 `/v0/resource/plugins/cpa-quota-sentinel/status`，填入 CPA 管理密钥。
5. 在“调度设置”打开低额度保护，确认阈值后保存，再刷新账号额度并查看状态与日志。

从 0.4.3 升级：先停止 CPA，移除旧插件文件并将 CPA 配置中 `codex-quota-scheduler` 的配置复制到 `cpa-quota-sentinel`，再安装新版本并启动。不要同时加载两个版本。插件沿用旧状态目录；旧的管理入口会变更。CPA 配置弹窗可能显示空白，留空仍使用默认路径，可手动填写该路径。

自定义路径可在 CPA 插件配置弹窗设置，也可直接写入 CPA 的 `config.yaml`。
字段直接放在插件配置下，不增加 `config:` 嵌套：

```yaml
plugins:
  enabled: true
  configs:
    cpa-quota-sentinel:
      enabled: true
      priority: 1
      management_path: /my-private-quota
```

修改后使用：

- 页面：`/v0/resource/plugins/cpa-quota-sentinel/my-private-quota/status`
- 鉴权 API：`/v0/management/my-private-quota/status?format=json`

宿主重新注册路由后旧地址失效。自定义路径减少默认入口被发现的机会，仍需管理密钥、HTTPS 与宿主限流。
公开资源仅为静态页面壳，账号数据和写操作需要管理密钥。
CPA 的插件配置弹窗不支持原生动作按钮，“打开后台”链接位于插件的设置页面。

### 额度保护配置

| 字段 | 默认值 | 用途 |
| --- | --- | --- |
| `quota_protection_enabled` | `false` | 在调度设置中明确开启。 |
| `five_hour_remaining_threshold` | `20` | 已知 5 小时窗口剩余百分比小于等于此值时暂停。 |
| `long_window_remaining_threshold` | `20` | 已知周/月窗口剩余百分比小于等于此值时暂停。 |
| `quota_recovery_threshold` | `30` | 恢复需达到此值，且必须大于两个暂停阈值。 |
| `quota_protection_interval` | `5m` | 启用保护时的后台额度检查间隔，包含暂停账号恢复复核。 |
| `quota_auto_recover` | `true` | 仅恢复插件拥有明确所有权的账号。 |

页面已保存的调度设置优先于启动 YAML，`management_path` 始终由 CPA 插件配置控制。
恢复先等待触发保护窗口的重置时间，再核实已记录及新出现的窗口。
重置时间未知、已有窗口缺失、凭据轮换或所有权丢失时停止自动恢复。
宿主不提供 auth 原子比较写接口；写前重读可缩小与人工改动的并发窗口，但不能完全消除。

## 上游功能参考

以下为继承的上游调度说明；如与新增保护策略有差异，以前述规则为准。
上游发布下载不包含本仓库的二次开发功能。

## 上游版本记录

v0.4.6 更新插件展示名称，并在管理页加入后台路径快捷入口。
完整更新历史见 [CHANGELOG.zh-CN.md](CHANGELOG.zh-CN.md)。

## 调度逻辑

调度器依次执行四层判断。每一层都会筛选或排序账号，再把结果交给下一层。

### 1. 接管 CPA 优先级层

- 只考虑 provider 为 `codex` 的候选账号，忽略其他 provider。
- 没有显式 CPA 账号优先级的 Codex 账号按优先级 `0` 处理。
- 开启 `schedule_across_priorities`（默认开启；v7.3 宿主会同时提供全部层级的
  候选）时，插件接管所有 CPA 优先级层级的 Codex 账号。更高层级只要还有可选
  账号就一定优先；只有更高层级全部不可用时才会选用低层级，而不是交给 CPA
  内置 fallback。低层级账号同样会刷新额度，以便掌握其可用性。
- 关闭 `schedule_across_priorities`，或宿主只提供最高层级候选时，插件只接管
  当前已确认的最高 CPA 优先级层，较低层仍由 CPA 自己的 fallback 逻辑处理。
- 如果希望所有 Codex 账号一起参与调度，应为它们设置相同的 CPA 账号优先级。
  最简单的推荐配置是全部使用优先级 `0`。

CPA 账号优先级与插件自己的账号优先级是两个独立设置。插件不会从 CPA 读取
插件优先级，也不会把插件优先级写回 CPA。

### 2. 先判断账号是否真的可用

进入调度范围的账号会被分成三个实际类别：

1. **可直接使用：**额度信息新鲜或仍在允许范围内，而且账号当前可用。
2. **可安全试用：**额度证据未知或已经过期，但能够在使用前进行安全验证。
3. **不可用：**长周期额度已耗尽、认证被阻止、熔断器已打开、临时耗尽反馈仍
   有效，或当前无法安全验证账号。

可直接使用的账号先于可安全试用的账号。插件不会选择不可用账号。

账号必须拥有有效的长周期额度（周额度或月额度）。五小时额度窗口是可选的：
如果 OpenAI 暂时不返回五小时额度，只要长周期额度有效，账号仍然可以使用。
如果长周期额度缺失或无效，账号保持未知/不可用，由 CPA fallback 接管。

如果账号同时存在长周期额度耗尽和较早记录的临时耗尽反馈，周额度或月额度
耗尽是权威原因，管理界面也会显示这个真实原因。

### 3. 对可用账号排序

只有完成可用性分类之后，插件优先级才参与排序，而且只在同一个可选择类别
内部生效。插件优先级较高的账号会先被考虑，但插件优先级不能把不可用账号
移动到可用账号前面。

插件优先级相同时：

1. 如果 `monthly_mode` 为 `priority`，月度账号排在周度账号前面。
2. 额度压力更高的账号排在前面。额度压力为“长周期剩余额度百分比 ÷ 距离
   长周期重置的小时数”，并使用 30 分钟作为最小除数。
3. 额度压力相同或无法计算时，依次按更早到期、更多剩余额度打破平局。
4. 最后使用稳定的账号 ID 顺序打破平局。

默认的 `monthly_mode: expiry_order` 会让周度和月度账号共同按“剩余额度和
重置时间组合出的额度压力”排序。可选的 `priority` 模式会先明确优先使用
月度账号，再比较额度压力。

### 4. 对不可用账号排序

账号不可用后会忽略插件优先级，因为优先级无法让一个已耗尽的账号恢复可用。

管理界面把不可用账号按预计恢复时间从早到晚排列。无法确定恢复时间的账号
放在最后。这样，队列反映的是哪个账号真正可能最先恢复，而不是保留已经没有
调度意义的优先级顺序。

如果“可直接使用”和“可安全试用”两组中都没有安全选择，插件不会返回账号，
并允许 CPA fallback。

### 排序示例

假设当前最高 CPA 优先级层中有四个 Codex 账号，实际显示和调度顺序如下：

| 顺序 | 状态 | 插件优先级 | 恢复时间 | 排序原因 |
| --- | --- | ---: | --- | --- |
| 1 | 可直接使用 | 10 | — | 在可直接使用的账号中优先级最高 |
| 2 | 可直接使用 | 0 | — | 账号仍然可用，因此排在所有不可用账号之前 |
| 3 | 周额度耗尽 | 100 | 2 小时后 | 账号不可用，忽略高优先级；它是最早恢复的不可用账号 |
| 4 | 已耗尽或未知 | 1000 | 更晚或未知 | 账号不可用，而且预计最后恢复或无法确定恢复时间 |

## 额度刷新与新周期激活

### 额度刷新

额度刷新从 ChatGPT 读取当前 Codex 额度状态。它不会发送普通模型请求，也不需要
一直打开管理页面。

额度数据有两个来源：

- **响应内观测（优先）。** 每条 Codex 响应流都带有 `codex.rate_limits` 事件，其
  窗口数据与额度端点一致；成功的 usage 记录在 WebSocket 传输下还会携带宿主归一
  化的 `X-Codex-*` 快照。插件注册了一个严格只读的流分片拦截器来观测这些帧，将
  其归属到实际服务的账号，并作为真实流量的副产品刷新额度缓存。观测同时会推迟
  该账号的轮询刷新，并为临时耗尽的和解提供绑定真实响应的证据。拦截器绝不修改、
  缓存或延迟任何流内容。
- **轮询兜底。** 近期没有观测到的账号按原有节奏从通用额度端点刷新。

管理界面会显示每个账号的额度来源（响应观测 / 轮询刷新）。

最近观察到 Codex 活动时，各账号只在自己的刷新期限到达后才会刷新；后台不会
按固定全局间隔反复扫描全部账号。活跃窗口结束并进入空闲后，普通后台刷新会
休眠，直到 Codex 请求、管理操作或到期的新周期操作将其唤醒。

收到 `usage_limit_reached` 响应后，插件会立即把刚才选择的账号标记为临时耗尽，
直到响应中的重置时间；如果响应没有重置时间，则临时阻止两分钟。额度耗尽不会
计入熔断失败。重复出现的非额度错误才由熔断器处理。

### 临时耗尽的恢复

临时耗尽标记只在有可信证据时才会清除，绝不单凭通用额度百分比清除：

- **经过该账号的真实请求成功**会立即清除标记。若之后再次收到
  `usage_limit_reached`，会按新的重置时间重新标记。
- **任何成功的额度刷新，在快照携带严格重置证据时会自动清除标记：**所有已知
  窗口都有剩余额度，且 5 小时窗口的重置期限相比标记时已发生变化（上游重置会
  生成新窗口）。没有 5 小时窗口时，接受“长周期窗口可用且重置期限晚于标记期限”
  作为较弱的回退证据。
- **在管理页面手动刷新单个账号**时，若最新额度快照显示所有已知窗口都有剩余
  额度，则清除标记——即使读数来自同一窗口也生效，因为操作员已显式确认上游
  重置。手动刷新还会越过宿主侧的 `disabled`/`unavailable` 冷却标记，因此在
  操作员触发上游重置后，即使 CPA 仍保留自己的冷却，账号也能完成刷新。
- **后台刷新绝不单凭同窗口的百分比清除标记。** 通用额度端点可能在 429 所指的
  同一个窗口上显示剩余 100%，而模型请求仍被上游 429（在 K12 计划账号上观察
  到），因此缺少上述窗口身份变化的同窗口满额读数不构成恢复证据。

### 托管停用与恢复（可选）

开启 `enable_managed_quota_disable` 后，收到确认的 `usage_limit_reached` 会把该
账号的 CPA 凭据标记为 `disabled: true`，CPA 自身即停止向它路由。插件保存按
auth index 键控、携带凭据指纹的持久所有权记录，并在后续只读额度检查证实两个
额度窗口均可用后才自动恢复启用。

安全规则：

- 人工手动停用的账号绝不被收养或重新启用；
- 恢复绝不单凭时间流逝启用；
- 所有权指纹必须仍然匹配——同一 auth 文件下的轮换凭据交由操作员处理；
- 因崩溃中断的 planned 记录会在下一轮恢复中完成对账；
- 该功能默认关闭，因为插件会写宿主认证文件。

账号卡片会显示托管停用状态与下次检查时间。移植自 dos1989 的
managed-quota-recovery 分支（MIT），并补齐了指纹匹配、5 小时窗口证据与崩溃
对账。

### 新周期激活

OpenAI 有时会显示额度重置时间已经到达，但在账号再次发送 Codex 请求之前不会
生成新的额度周期。开启自动新周期激活后，插件会：

1. 再次检查当前额度；
2. 只有确认新周期仍未生成时，才发送一次极小的 Codex 请求；
3. 请求后重新读取额度并验证结果；
4. 持久化操作状态，发生重启时先验证，再决定是否重试。

该功能默认关闭，因为激活请求可能消耗少量额度。多个并发触发会共享同一个操作，
不会重复发送激活请求。

### CPA 暂时无法确认账号列表时

CPA 无法确认当前 Codex 账号列表和优先级时，普通刷新和新周期激活会停止。插件
可以继续提供安全的管理信息，同时重试账号列表同步。

高风险设置 `probe_on_provisional_roster` 允许在这种情况下，使用最近一次保存的
账号列表尝试新周期激活。每次尝试前都会重新验证账号凭据，但插件仍无法保证
账号没有被删除，也无法保证账号没有被移动到其他 CPA 优先级层。除非明确理解并
接受该风险，否则应保持关闭。

## 功能

- 面向 CPA Codex 账号的优化版 Fill First 调度。
- 生产选择与管理队列都按真实可用性优先排序。
- 支持周额度和月额度，五小时额度窗口可选。
- 处理 `usage_limit_reached` 额度耗尽反馈。
- 账号级故障熔断器。
- 按期限驱动的额度刷新，以及可选的新周期激活。
- 支持浏览器语言检测的中英文管理界面。
- 账号别名、备注、标签、分组和插件优先级。
- 调度设置与账号标注的 JSON 导入和导出。
- Linux、macOS、Windows 和 FreeBSD 发布包。

## 安装

### 方式一：通过 CPA 插件商店安装（自定义插件源）

CPA 插件商店通过读取 `registry.json` 索引源，并从对应 GitHub 仓库的 Releases 下载安装包。

1. 在 GitHub 仓库推送 `v*` 版本标签（例如 `v0.4.12`），触发 GitHub Actions 自动编译各平台 `.zip` 包与 `checksums.txt` 并发布到 GitHub Releases。
2. 在 CPA 的 `config.yaml` 中将本仓库的 `registry.json` 加入 `plugins.store-sources`：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/While-Shark/CPA_Quota_Sentinel/master/registry.json"
```

3. 打开 CPA 管理后台的插件商店，搜索 **CPA_Quota_Sentinel**（注意选择自定义源），点击安装即可。

### 方式二：手动安装 / 本地编译安装

从 [最新 GitHub Release](https://github.com/While-Shark/CPA_Quota_Sentinel/releases/latest) 下载对应平台的压缩包（或直接使用本地编译生成的 `dist/cpa-quota-sentinel.dll` / `.so` / `.dylib`）：

```text
cpa-quota-sentinel_<version>_<goos>_<goarch>.zip
```

从压缩包根目录解压动态库，并放入 CPA 对应平台的插件目录：

- macOS：`cpa-quota-sentinel.dylib`
- Linux 和 FreeBSD：`cpa-quota-sentinel.so`
- Windows：`cpa-quota-sentinel.dll`

示例：

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/darwin/arm64
cp cpa-quota-sentinel.dylib /path/to/CLIProxyAPI/plugins/darwin/arm64/
```

## CPA 配置

全局启用插件，并启用本插件：

```yaml
plugins:
  enabled: true
  configs:
    cpa-quota-sentinel:
      enabled: true
      priority: 1 # CPA plugin registration/load priority
```

这里的注册/加载 `priority` 不是 CPA 账号优先级，也不是插件自己的单账号调度
优先级。账号标注和调度设置通过插件页面管理，不使用 CPA 的通用插件表单。

默认调度设置：

```yaml
handle_enabled: true
quota_refresh_interval: 30m
stale_after: 5h
refresh_active_window: 1h
refresh_after_reset_delay: 1m
refresh_retry_delays: 1m,5m,15m
auth_failure_retry_interval: 30m
refresh_on_startup: false
monthly_mode: expiry_order
fallback: fill-first
enable_usage_feedback: true
enable_reset_probe: false
probe_on_provisional_roster: false
max_refresh_concurrency: 1
quota_endpoint: https://chatgpt.com/backend-api/wham/usage
circuit_failure_threshold: 5
circuit_open_duration: 30m
circuit_half_open_success_threshold: 2
max_log_entries: 200
log_retention: 24h
```

`monthly_mode` 可选值：

- `expiry_order`：周度账号和月度账号共同按到期时间排序。
- `priority`：在同一个可选择类别和插件优先级中，月度账号排在周度账号前面。

`quota_endpoint` 被限制为预期的 ChatGPT 额度端点，不能改为任意主机。

### 认证失败自动恢复

当账号的 token 刷新收到 401/`invalid_grant` 时，账号会被排除出调度，但不再永久停机：

- 账号会按 `auth_failure_retry_interval`（默认 30 分钟）低频重试 token 刷新，
  重新登录后无需手动刷新，退避时间一到即自动恢复；
- 凭据对账还能直接识别操作员重新登录（同一认证文件下出现新的 refresh token）：
  立即清除认证异常停机并触发一次验证刷新，恢复不必等待退避；
- 只有该次刷新成功后账号才重新参与调度，恢复始终携带新的额度证据；
  验证失败（例如重新登录未生效）只会重新停机等待下次重试。

## 模型重试链

管理界面包含一个可选的重试链，用于处理上游容量与传输类失败（采纳自 doer-ee 的
Codex Fleet Manager，MIT 许可）。开启后，流式请求在内容尚未发给客户端之前失败时，
可以按配置的备用模型链继续尝试。可重试的失败包括 HTTP 429、500、502、503、504、
529、容量/超载类失败以及等价的传输失败；无法识别的失败一律不重试（fail-closed），
内容已经发给客户端的请求绝不重试。每次重试都通过宿主重新发起，因此账号仍由正常
调度器选择，用量反馈也照常生效。

在侧边栏展开「模型重试链」折叠面板。每个被请求的模型都可以配置一组有序的备用
目标；提供方由 CPA 解析（可留空）。备用目标按每个模型独立编号。

重试模式有三种：**生效**、**影子**（只记录会重试的情况，不真正重试）和**全局
重试所有模型**。全局重试也覆盖没有配置链的模型；没有备用目标时重试同一模型。
影子与全局重试互斥。面板还可配置最大尝试次数（含首次）、静默/等待/整链超时、
帧与字节缓冲上限，以及切换模型时是否移除加密推理内容。

面板可以检查并修复 CPA 前置配置，但要求 CPA `v7.3.4` 或更高版本。配置不符时
会显示当前值与推荐值的对照表（差异标红）；只有点击「应用推荐设置」才会修改。
修复通过 CPA 热重载生效，无需重启容器：

```yaml
request-retry: 3
codex:
  stream-bootstrap-buffering: true
  stream-bootstrap-timeout: "0"
streaming:
  bootstrap-retries: 1
```

重试事件会写入插件日志，并提供中英文双语。重试链默认关闭，需要显式开启。

## 管理界面

从 CPA Management Center 打开 **Codex Scheduler**，或者访问：

```text
/v0/resource/plugins/cpa-quota-sentinel/status
```

页面提供：

- 与生产调度一致的账号队列和下一账号预览；
- 将账号队列、调度设置和模型重试链分成独立页面，不再把所有设置放在中间列；
- 分别显示 CPA 优先级和插件优先级；
- 额度条、重置时间、不可用原因和熔断状态；
- 带有通俗安全说明的调度设置；
- 别名、备注、标签、分组和单账号插件优先级编辑；
- 额度刷新、日志查看/导出以及配置导入/导出；
- 打开模型重试链页面时自动载入并去重可路由的模型 ID；
- 中英文界面切换。

CPA 插件菜单 API 只能注册一个静态名称，因此所有管理界面的侧边栏统一显示
**Codex Scheduler**。

嵌入 CPA Management Center 时，插件初次会跟随 CPA 当前语言：中文 locale 使用
中文，其余 locale 默认使用英文。如果曾在插件内手动选择语言，该选择会被记住，
后续访问时优先使用。

受保护的数据和操作需要 CPA 管理密钥。默认情况下，密钥只保留在当前浏览器页面
会话中。可选的「在此浏览器中记住管理密钥」设置会以未加密形式将密钥保存到
浏览器本地存储，并在以后访问时自动加载受保护数据。请仅在受信任的设备上启用。
密钥不会写入插件状态、导出文件或日志；取消勾选会删除浏览器中保存的副本。

## 致谢

本版本整合了插件 fork 社区的工作（均为 MIT 许可）：

- **doer-ee / Codex Fleet Manager**：按模型重试链 failover、影子模式与 CPA
  前置检查、管理密钥显示体验、账号置顶、探测模型更新。
- **dos1989**：托管停用与恢复的概念及所有权记录设计。
- **Siriussee**：模糊发送的恢复调度与外部补偿性重置的 rebase 行为。
- **jacobhere（PR #4–#10）**：额度压力调度、探活端点修复、重置倒计时、界面
  本地化、英文侧栏标签、记住管理密钥、额度条颜色分档。
- **lawyer61（PR #12）**：inflight 限制的设计（见追踪 issue #13）。

## 隐私与数据说明

插件在 CPA 进程内部运行，使用 CPA host callback 和插件自己的 CPA Management
API 路由。插件不运行外部服务，也不会向插件作者发送数据。

插件可能使用已经配置在 CPA 中的 Codex 凭据，向以下端点发送认证请求：

```text
GET https://chatgpt.com/backend-api/wham/usage
GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits
```

开启新周期激活后，插件还可能发送前文说明的极小 Codex 激活请求。

本地插件状态可能包含调度设置、额度快照、操作状态、日志、别名、备注、标签和
分组名称。不要在备注、别名、标签或分组标注中填写秘密。管理界面避免渲染
access token、Authorization header、Cookie 和其他凭据字段。

Resource 路由只提供界面资源。账号数据和受保护操作通过 Management 路由处理，
并要求 CPA 管理密钥。

## 构建

要求：

- `go.mod` 中声明的 Go 1.26 或更高版本。
- CGO 支持，以及用于 `-buildmode=c-shared` 的 C 编译器。
- 用于跨平台发布流程的 `make`。

运行测试：

```bash
make test
```

构建当前平台的动态库：

```bash
make build
```

构建发布压缩包和校验文件：

```bash
make package VERSION=0.4.12
make checksums VERSION=0.4.12
```

Windows 用户可以用以下命令构建 `dist/cpa-quota-sentinel.dll`：

```powershell
.\build.ps1
```

## GitHub Release

推送 `v0.2.1` 这类点分数字标签后，GitHub Actions 会运行发布流程。流程会测试
仓库，并发布各平台压缩包和 `checksums.txt`：

打 tag 之前，请在 `CHANGELOG.zh-CN.md` 记录本次发布，并同步更新
`config.go`、`Makefile` 和两份 README 中的版本字符串。

```bash
git tag -a v0.4.12 -m "v0.4.12"
git push origin v0.4.12
```

发布包使用以下命名方式：

```text
cpa-quota-sentinel_<version>_<goos>_<goarch>.zip
```

## Management API

界面资源路由：

```text
GET /v0/resource/plugins/cpa-quota-sentinel/status
```

受保护操作需要 CPA 管理密钥：

```text
GET  /v0/management/plugins/cpa-quota-sentinel/status?format=json
GET  /v0/management/plugins/cpa-quota-sentinel/logs
GET  /v0/management/plugins/cpa-quota-sentinel/export
PUT  /v0/management/plugins/cpa-quota-sentinel/settings
POST /v0/management/plugins/cpa-quota-sentinel/refresh
POST /v0/management/plugins/cpa-quota-sentinel/refresh/account
POST /v0/management/plugins/cpa-quota-sentinel/import
PUT  /v0/management/plugins/cpa-quota-sentinel/annotations
PATCH /v0/management/plugins/cpa-quota-sentinel/annotations/account
PATCH /v0/management/plugins/cpa-quota-sentinel/annotations/group
```

## 许可证

MIT License。参见 [LICENSE](LICENSE)。
