# UpDNS 使用与开发指南

**简体中文** | [English](./GUIDE.en.md)

[← 返回 README](../README.md) · [项目仓库](https://github.com/garinasset/updns)

本文面向 UpDNS 用户和维护者，集中说明产品行为、配置方式、构建流程，以及 LuCI 页面与后端配置的对应关系。

## 产品概览

UpDNS 是一个 OpenWrt 的 DNS 服务。它通过 UDP/TCP 接收查询，根据域名列表规则选择上游 DNS，并可将解析地址写入指定 nftables set。

支持的上游协议：UDP、TCP、DNS over TLS（DoT）和 DNS over HTTPS（DoH）。可单独使用 UpDNS，也可将它配置为 dnsmasq 等 DNS 转发器的上游。

主要能力：

- **规则**：按域名列表匹配，支持动态调度、严格顺序和并发查询策略、地址族过滤、NXDOMAIN 响应和 Zero-IP 拦截（返回 0.0.0.0 / ::）等。
- **本地解析**：优先回答 hosts 和 OpenWrt DHCP 本地记录，再查缓存和上游；本地数据仍遵守规则。
- **缓存**：支持容量限制及主动预取。
- **主动预取**（默认启用）：后台每 100 毫秒调度已消耗至少 80% TTL 的条目，包括已过期条目；
- **nftables**：可分别将解析得到的 IPv4 和 IPv6 地址加入规则指定的集合；也可选择加入规则所引用上游 DNS 端点的 IP 地址。
- **LuCI**：通过 Web 界面管理服务、上游、域名列表、缓存、日志和规则。
- **CLI**：`updns <域名>` 查看查询结果、命中的规则、缓存/上游路径及 nftables 集合状态。

## 配置与规则

OpenWrt 配置使用 UCI 格式：

- 主配置：`/etc/config/updns`
- 域名列表：通常存放于 `/etc/updns/domainlists/`，每行一个域名
- 默认端口：`153`，同时监听 UDP 和 TCP

软件包默认配置五个上游：Tencent-UDP、Google-DoH、Cloudflare-DoH、Tencent-DoH 和 AdGuard-DoH。启用本地 resolver 文件时，文件中的 nameserver 也会作为运行时 UDP 上游；默认文件为 `/tmp/resolv.conf.d/resolv.conf.auto`。

“默认上游”仅指随包配置文件中预先配置的上游，程序不内置任何上游 DNS。上游列表及顺序完全以配置文件为准，可修改或删除，删除全部也不会自动补回。配置文件不存在时明确报错，不使用回退配置。本地 resolver 自动导入由其开关独立控制。

上游地址格式：

| 协议 | 地址示例 | 默认端口 |
|---|---|---:|
| UDP | `1.1.1.1` 或 `1.1.1.1:5353` | 53 |
| TCP | `1.1.1.1` 或 `1.1.1.1:5353` | 53 |
| DoT | `dns.google` 或 `dns.google:853` | 853 |
| DoH | `https://dns.google/dns-query` | 443 |

DoH 地址须为完整 HTTPS URL。IPv6 地址带端口时使用方括号，例如 `[2001:db8::1]:5353`。

### 域名列表

列表项由唯一名称和文件路径组成；规则通过列表名称引用文件; 列表文件格式为每行一个域名。匹配不区分大小写，空行和以 `#` 开头的行会忽略。

域名匹配时会匹配精确域名及其子域名，
`example.com` 可以匹配 `example.com` 和 `www.example.com`，但不能匹配 `superexample.com`；通过在开头添加 `*` 可以改变这一行为：`*example.com` 可以匹配 `example.com`、`www.example.com`，以及 `superexample.com`。

### 规则行为

规则按顺序评估，命中第一条适用规则即停止。一个规则可引用多个域名列表，命中其中任一个列表即满足域名条件。LuCI 支持拖动普通规则排序；在规则的“如果域名匹配”下拉框中选择“不执行匹配（兜底规则）”（对应 `domain_match='0'`），该规则将被视为兜底规则，兜底规则最多一条且总是在普通规则之后执行。如果没有兜底规则, 且请求未命中任何普通规则时，使用默认上游。

| 操作 | 行为 |
|---|---|
| `ipv4+ipv6` | 返回 IPv4 和 IPv6 结果 |
| `ipv4only` | 仅返回 IPv4 结果 |
| `ipv6only` | 仅返回 IPv6 结果 |
| `nxdomain` | 直接返回 NXDOMAIN，不查询上游，缓存该响应 |
| `zeroip` | Zero-IP 拦截：A 查询返回 `0.0.0.0`，AAAA 查询返回 `::`，不查询上游，缓存该响应 |

`nxdomain` 与 `zeroip` 响应的缓存 TTL 固定为 60 秒，且不写入 nftables set。

`ipv4+ipv6`、`ipv4only`、`ipv6only` 只过滤 A/AAAA 记录；MX、TXT、NS、SOA、SRV、PTR 等非地址记录及 CNAME 原样返回。HTTPS/SVCB 记录照常返回，`alpn`、`ech` 等参数保留。

HTTPS/SVCB 处理规则：

- **地址提示：** `ipv4hint`/`ipv6hint` 按规则动作过滤（如 `ipv4only` 移除 `ipv6hint`），保留的提示地址与 A/AAAA 一样写入规则的 nftables set。
- **目标名绑定：** 响应中的目标主机名（TargetName，ServiceMode 为 `.` 时即查询名本身）绑定到产生该响应的规则。客户端随后查询目标名时，使用该规则的上游和动作，并将地址同时写入所有绑定规则及目标名自身匹配规则的 set，确保 nftables 路由链一致。多个规则绑定同一目标时，上游和动作取首个绑定规则。
- **绑定生命周期：** 绑定按记录 TTL 过期，最短 30 秒；容量与 `cache_size` 相同，已满时丢弃新的 HTTPS/SVCB 记录，不淘汰有效绑定。绑定后异步预取目标名地址，不同步追加查询。
- **限制：** 目标名自身命中 `nxdomain`/`zeroip` 规则，或链长（含 CNAME）超过 16 跳时，丢弃该 HTTPS/SVCB 记录，避免绕过拦截。未命中规则（使用默认上游）的查询不建立绑定；错误码响应原样返回，但不建立绑定，提示地址也不写入 set。

规则（`nxdomain`、`zeroip` 除外）可选择一个或多个上游，并为 IPv4/IPv6 分别指定 nftables set。

规则还可选择把其引用上游 DNS 端点的 IP 地址加入对应 nftables set。主机名（包括 DoH URL 主机名）会解析为 IP，并定期刷新；新地址会添加到集合中，不会自动从共享集合删除旧地址。

### 本地解析

UpDNS 直接读取 `/etc/hosts`、`/tmp/hosts/` 下的 hosts 文件和 DHCP 租约，支持 A、AAAA、PTR 和 ANY。本地域名及路径取自 `/etc/config/dhcp` 的第一个 dnsmasq 配置段：`domain`、`expandhosts`、`leasefile`、`addnhosts` 和 `hostsdir`；也读取 odhcpd 的 `hostsdir`。无 OpenWrt DHCP 配置时，默认租约路径为 `/tmp/dhcp.leases`、域名后缀为 `lan`，不自动扩展 hosts 短名称。

本地记录优先于旧的上游缓存，hosts 优先于同名 DHCP 租约；租约到期不再使用。文件、目录和本地域名设置在查询时检测更新，解析结果使用 TTL 0，不写入 DNS 缓存、不做周期预取；日志和 CLI 显示 `Local Answer`。本地记录不绕过 NXDOMAIN/Zero-IP 拦截，也遵守地址族过滤和 nftset 规则。本地数据文件无法读取或解析时记录限频告警，并继续使用缓存和上游解析。`use_local_resolv` 只控制从 resolv.conf 导入上游地址，与本地记录解析无关。

### 上游 DNS 选择策略

`query_strategy` 仅接受 `0`、`1`、`2`，默认 `0`；不兼容原单词配置值：

- **动态调度（0）**：同一个有序上游组首次并发选取能响应的上游，后续通常只查询该上游。完成 50 次单上游选择后，或距离上次重评达到 20 秒，在下一次初始解析时重新并发评估；没有独立后台探测。原上游传输失败或响应无效时重新选择。名称表示自适应优选，不保证始终延迟最低。
- **严格顺序（1）**：按引用顺序逐个尝试，直到获得有效最终响应。
- **并发查询（2）**：并发请求所选上游，以首个有效最终响应胜出，取消其他在途请求。

三种策略统一适用于 UDP/TCP/DoT/DoH，保留本项目各上游超时、CNAME 补查和取消机制。动态调度借鉴 [dnsmasq 官方手册](https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html)及其 [2.93 发布版](https://thekelleys.org.uk/dnsmasq/dnsmasq-2.93.tar.gz)的优选设计，但不照搬客户端驱动重试或协议分支差异。上游配置变化和清空缓存会重置优选状态。

有效缓存直接返回，不产生上游查询。过期刷新和主动预取优先使用该条目原胜出上游，不参与常规组重评；仅当原上游不可用或响应无效时，按所选策略尝试当前组其他上游，成功后更新条目路径。规则或服务绑定发生变化时重新校验路由，不固守旧策略。没有匹配规则且没有兜底规则时，使用整个上游列表；本地 resolver 上游排在其他上游之前。

三种策略遵循相同的响应判定与缓存规则：

- **响应有效性：** 响应须匹配请求的 ID、操作码、问题域名（不区分大小写）、类型和类，并设置响应标志。有效性不受 RCODE 限制，`NOERROR`、`NXDOMAIN`、`FORMERR`、`SERVFAIL`、`REFUSED`、`NOTIMP` 等有效响应均可胜出。错误码响应即使省略 Question，也保留实际 RCODE 和 Question 数量用于诊断。
- **CNAME 处理：** `NOERROR` 响应须包含所查类型记录，或完整的 CNAME 链及其终点结果。遇到未完成的 CNAME 链，在同一上游补查链终点，沿用原规则、超时预算和取消信号，不重新匹配规则。最多允许 16 跳，并检测跨响应循环。仅含 NS 委派、无 SOA 的 referral 不是终点结果；`NXDOMAIN` 不得包含所查类型的终点记录。
- **缓存资格：** 响应胜出后独立判断缓存资格，不影响胜出及客户端返回。正向记录按 TTL 缓存；NODATA 和 NXDOMAIN 须包含覆盖终点名称的有效 SOA，方可进行负缓存。缺少 SOA 或不符合缓存规范的响应仍正常返回，但不写入缓存。错误码响应不缓存，其中的地址也不写入 nftables set。
- **并发查询：** 首个有效最终响应胜出后，立即取消其他上游请求：UDP/TCP/DoT 关闭在途连接，DoH 取消 HTTP 请求。已经发到远端的请求无法撤回。
- **截断处理：** UDP 返回 `TC=1` 时，使用同一上游地址和端口进行 TCP 补查，共用超时预算；补查失败或仍然截断时不能胜出。TCP、DoT、DoH 的截断响应也不直接使用。
- **失败处理：** 所有上游均未获得有效最终响应时，普通查询返回本地生成的 `SERVFAIL`，HTTPS/SVCB 等保留原有失败抑制策略；具体失败原因通过 warn 日志记录。已胜出的有效 DNS 响应不进入失败处理。

### 缓存

正向缓存按响应记录的 TTL 管理；负向缓存使用响应 SOA 提供的 RFC 2308 TTL。缓存命中返回递减后的 TTL，NODATA 和 NXDOMAIN 须包含覆盖终点名称的有效 SOA，方可进行负缓存。缺少 SOA 或不符合缓存规范的响应仍正常返回，但不写入缓存。错误码响应不缓存。

收到有效的 NXDOMAIN / NODATA 后，先将响应 SOA 记录自身的 TTL 归一为 `min(SOA TTL, SOA.MINIMUM)`，首次下游响应与入缓存的 SOA 均使用该值；缓存命中按存储经过时间继续递减，不会因读取而重置 TTL。CNAME 链较短时可以使整包缓存提前到期，但不会改变 SOA.MINIMUM 字段；正向答复和直接查询 SOA 记录不做负缓存 TTL 归一化。

#### HitCount

缓存键为小写完整域名与查询类型，A、AAAA 独立缓存。条目 HitCount 初始为 0，每次有效缓存命中加 1（包括 CLI）；首次上游查询、预取及等待预取不增加命中数。更新条目继承当前版本的全部命中数，包括刷新期间收到的命中；计数饱和递增，不回绕、不做时间衰减。

不再保存删除或淘汰后的计数历史。只有仍存在的条目在刷新替换时继承 HitCount；删除、容量淘汰后重新添加同键条目从 0 开始。

#### 添加、删除与生命周期

不运行定时过期清理。新增条目容量不足时只淘汰一个 HitCount 最低的条目，不优先回收过期条目；同计数时淘汰最久未被客户端命中（未命中过则按首次添加顺序）的条目。后台刷新不会提升此同频次优先级。因此，高命中的过期条目可能保留而淘汰有效冷条目。

TTL 到期不立即删除，也不允许返回旧数据：保留条目用于调度预取和继承命中数。关闭主动预取时，过期条目可保留到容量回收或下一次请求重新解析；缓存数量表示总占用，包含过期条目，不等于可直接应答的数量。

缓存仅驻留内存，不跨进程持久化。显式清空、停止、重启及 OpenWrt 配置重载（实际重启）清除缓存和任务状态。每次替换使用单调版本号，清空使用代次号，旧任务既不能删除/覆盖新版本，也不能复活已淘汰或已清空条目。

#### 主动预取与请求合并

每 100 毫秒检查调度队列，已消耗至少 80% 原 TTL（剩余有效期不大于 20%）的条目进入待预取池，包含剩余 TTL 归零的旧条目。待预取池不含已在途任务；按原过期时间从早到晚启动，全局最多 8 个后台任务同时在途，每轮补足空闲名额，慢任务不阻塞下一轮扫描。所有条目均可预取，不以 HitCount 或当前访问量过滤。

- 未过期：立即返回递减 TTL 的缓存，路径为 `Cache Hit`。
- 已过期且正在预取：等待同一个任务完成，路径为 `Prefetch Hit`；成功返回新答复，失败返回失败，绝不返回过期缓存。日志及 CLI 保留此路径。该名称表示复用预取结果，不计作缓存命中。
- 已过期但尚未预取，或完全未缓存：由前台立即启动解析，路径为 `Upstream Query`；不受后台 8 个名额限制。相同键的其他前台请求及后台扫描复用该在途任务，不重复查询。

刷新成功且 TTL 大于 0 时替换对应旧版本并继承 HitCount。刷新失败、TTL 为 0 或胜出响应不可缓存（如错误码、无 SOA 的否定响应）时：旧条目未过期则保留答复、原过期时间、原路径及计数，推迟到原过期时刻再预取，避免每 100ms 重试；旧条目已过期则删除，不返回过期答复、不保存计数历史，也不再后台重试该名称。后续客户端可重新解析，重新缓存后的 HitCount 从 0 开始。

有效不等于可缓存：上游原始 TTL 为 0、无 SOA 的 NOERROR/NXDOMAIN 及错误码响应均不入缓存、不进入周期预取；这些胜出响应仍交付等待者，保留原 RCODE。正常负缓存使用 `min(SOA TTL, SOA.MINIMUM)`；CNAME 链与终点否定结果共同缓存时，有效期还受 CNAME TTL 限制。并发/顺序查询仍沿用相同上游选择规则。后台 8 个任务不等于 8 个网络请求：上游并发及 CNAME/TCP 补查可能增加实际请求数；100 毫秒也不是完成时限。

在途结果为各等待者独立复制，避免并发修改同一 DNS 报文。网络 I/O 不持有缓存或调度锁。清空会取消已有任务并唤醒等待者；停止服务统一取消并等待调度循环和所有解析任务退出。

#### 数据结构与性能

缓存使用哈希表及三个索引最小堆：LFU 淘汰顺序、刷新时刻和已就绪任务的过期优先级，不再维护过期回收索引或历史池。命中优先级更新、添加/删除和单条调度为 O(log N)，空闲扫描为 O(1)；到期条目仅在状态转换时进入就绪队列，避免每 100 毫秒全量遍历、排序和复制 DNS 记录。所有索引随删除同步回收，无无界失效节点积累；总体空间 O(N)，在途任务表另按实际共享解析数计。

### 基本 UCI 示例

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option query_strategy '0'
    option cache_enabled '1'
    option cache_size '4096'
    option active_preload '1'
    option log_query '0'
    option log_level 'info'

config domainlist 'example'
    option name 'example-domains'
    option file '/etc/updns/domainlists/example.txt'

config rule 'example_rule'
    option name 'Example'
    option domain_match '1'
    list domain_lists 'example-domains'
    list upstreams 'Google-DoH'
    option action 'ipv4+ipv6'
    list ipv4_nftsets 'inet/fw4/example_v4'
    list ipv6_nftsets 'inet/fw4/example_v6'
    option add_upstream_ips '0'
```

完整默认配置见 [`openwrt/updns/root/etc/config/updns`](../openwrt/updns/root/etc/config/updns)。日志级别为 `debug`、`info`、`warn` 或 `error`；逐条 DNS 查询日志还需启用 `log_query`。

## LuCI 产品与前端参考

页面入口为 **服务 → UpDNS**，路由为 `/admin/services/updns`。当前页面分为运行状态、设置及端口、上游 DNS、域名列表、缓存、日志和规则等区域。

### 页面与配置模型

| 页面区域 | UCI section / 字段 | 产品行为 |
|---|---|---|
| 运行状态 | 服务状态、缓存状态文件 | 展示服务状态和缓存条目数，并定期刷新 |
| 设置及端口 | `updns.general`：`enabled`、`port`、`use_local_resolv`、`local_resolv_file`、`query_strategy` | 启停服务、监听端口、本地 resolver 和上游 DNS 选择策略 |
| 上游 DNS | `updns.upstream`：`name`、`address`、`protocol`、`timeout` | 管理 UDP/TCP/DoT/DoH 上游 |
| 域名列表 | `updns.domainlist`：`name`、`file` | 上传或管理列表文件，供规则引用 |
| 缓存 | `updns.general`：`cache_enabled`、`cache_size`、`active_preload` | 控制缓存、容量和主动预取 |
| 日志 | `updns.general`：`log_query`、`log_level` | 控制查询日志和日志级别 |
| 规则 | `updns.rule`：`name`、`domain_match`、`domain_lists`、`upstreams`、`action`、`ipv4_nftsets`、`ipv6_nftsets`、`add_upstream_ips` | 设置匹配条件、上游、返回类型及 nftables 处理 |

新安装默认 `enabled=0`，服务不会自动启动；默认端口 `153`、使用本地 resolver、动态调度、缓存启用且容量为 `4096`、主动预取启用、查询日志关闭。旧 `concurrent_query` 字段已移除，不做兼容映射；没有 `query_strategy` 时使用 `0`（动态调度），原单词配置值会报错，升级前需改为对应数字。

规则编辑页的“拦截”下拉框（不拦截 / NXDOMAIN 返回 / Zero-IP 返回）是 `action` 字段的界面控件，不是独立 UCI 字段：选择 NXDOMAIN 返回对应 `action='nxdomain'`，Zero-IP 返回对应 `action='zeroip'`；选择后隐藏并清除上游、nftables set 及“并返回”选项。

选择“不执行匹配（兜底规则）”的规则（`domain_match='0'`）会被作为兜底规则, 只能设置一条, 它将在没有命中所有其他规则之后被执行, luci 页面会把兜底规则顺序自动保持在普通规则之后。

### 前端文件及维护要点

| 内容 | 源码位置 |
|---|---|
| 页面视图、表单及状态展示 | [`config.js`](../openwrt/luci-app-updns/root/www/luci-static/resources/view/updns/config.js) |
| 菜单路由 | [`menu.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/luci/menu.d/luci-app-updns.json) |
| rpcd/ubus 权限 | [`acl.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/rpcd/acl.d/luci-app-updns.json) |
| 简体中文翻译 | [`updns.po`](../openwrt/luci-app-updns/po/zh_Hans/updns.po) |
| 后端默认 UCI 配置 | [`updns`](../openwrt/updns/root/etc/config/updns) |
| 包元数据和依赖 | [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile)、[`openwrt/updns/Makefile`](../openwrt/updns/Makefile) |

页面从 UCI 读取和保存配置；本地 resolver 项会显示为自动生成的 `local-resolv-N` 上游。服务状态来自 rpcd `service.list`，缓存条目数来自 `/tmp/updns_cache_size`，nftables 集合由页面调用 `nft -j list sets` 获取。域名列表通过 rpcd 文件接口读写。

修改页面时应同步检查相关 UCI 字段、rpcd ACL 和后端行为。ACL 采用明确的文件路径授权；增加新的状态文件、文件读写路径或系统命令时，需要一并更新 ACL。可见文本使用 `_()` 并同步维护 PO 翻译。若调整表单区域或 Tab 布局，需检查页面中的映射和排序逻辑。

## dnsmasq 集成

将 UpDNS 作为 dnsmasq 上游时，可添加本机转发地址：

```sh
uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#153'
uci set dhcp.@dnsmasq[0].cachesize='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

建议只在 UpDNS 启用缓存，避免双层缓存增加状态排查和结果更新延迟。两层缓存各自按 TTL 和缓存策略独立过期，并非必然重复延长 TTL。若希望 dnsmasq 仅使用 UpDNS，可设置 `noresolv='1'`。UpDNS 也可独立提供 DNS 服务；改用端口 `53` 前需确保该端口未被 dnsmasq 等程序占用。

## CLI

```text
root@OpenWrt:~# updns google.com

╭─ updns ──────────────────────────────────────╮
│
│  DOMAIN
│  ──────────────────────────────────────────
│  Domain       : google.com.
│  Query Type   : A / AAAA
│  Query Path   : Cache Hit
│
│  DNS RESULT
│  ──────────────────────────────────────────
│  Matched Rule : US Access
│  Upstream     : Google-DoH
│  Action       : IPv4 + IPv6
│  Response     : NOERROR
│
│  NFT SETS
│  ──────────────────────────────────────────
│  IPv4         : inet/fw4/us4
│                 ✓ Contains resolved IPv4
│  IPv6         : inet/fw4/us6
│                 ✓ Contains resolved IPv6
│
│  ANSWERS
│  ──────────────────────────────────────────
│  • 142.251.219.142
│  • 2607:f8b0:4005:818::200e
│
╰──────────────────────────────────────────────╯
```

服务运行时，CLI 通过本机控制接口查询，使用服务的规则、缓存并显示 nftables 集合信息；服务未运行时会独立查询，不使用服务缓存。查询默认分别执行 A 和 AAAA 查询。

`Query Path` 的 `Local Answer` 表示直接来自本地 hosts/DHCP；`Cache Hit` 表示有效缓存；`Prefetch Hit` 表示等待并复用预取任务；`Upstream Query` 表示前台解析。`Upstream` 在本地答复时显示 `local-hosts` 或 `local-dhcp`。

查询失败时仍会输出完整报告：`Response` 显示 `ERROR`，并新增 `ERRORS` 分区，按查询类型给出一行规范化的简短原因（如 `Upstream timeout`、`Upstream connection refused`、`Upstream host unresolvable`、`Network not ready`、`No upstream configured`、`All upstreams failed`），命令以非 0 退出码结束。仅 A 或 AAAA 之一失败时，另一类结果照常显示，只要有一类成功退出码即为 0。边框宽度自适应终端宽度（最小 48、最大 80 列）。

## 服务管理

服务由 OpenWrt init/procd 管理。配置中的 `general.enabled` 默认是 `0`, 保存并应用配置时会触发 init 脚本重新加载，并按该字段启动或停止服务进程。

```sh
/etc/init.d/updns enable
/etc/init.d/updns disable
/etc/init.d/updns start
/etc/init.d/updns stop
/etc/init.d/updns restart
/etc/init.d/updns reload
/etc/init.d/updns status
```
- 执行 `enable` 启用开机启动;

- 执行 `disable` 禁用开机启动;

- 执行 `start` 会启动服务进程, 如果服务进程已经在运行, 则直接返回;

- 执行 `restart` 等同于先 `stop`（停止进程并清理 nftables set）再 `start`；缓存随服务进程重启而重建。

- 执行 `reload` 
  - 在 `general.enabled=1` 时会走`重启`路径;
  - 在 `general.enabled=0` 时会走`停止`路径;

- 执行 `stop` 会停止服务进程（缓存随进程退出而释放），并在进程停止后清理当前配置所引用的 nftables set;

LuCI 保存并应用配置会触发 `reload`。

开机启动由 init 管理, 默认开机启动, 但配置中 `general.enabled` 默认为 `0` , 因此服务进程不会被启动; 当配置中 `general.enabled` 被设置为 `1`时, 如果启用开机启动, 服务进程也会被启动; 该配置升级时会保留原有配置, 不会被覆盖。

启用或停用服务时，建议通过 LuCI 操作，或同步维护 UCI 的 `general.enabled` 状态。

## 构建、安装与测试

### 本机开发

需要 Go 1.21 或更高版本。在 macOS/Linux 开发机上执行；本机编译的二进制用于开发和检查，不可直接安装到路由器。

```sh
make build
make test
make lint
```

二进制输出为 `bin/updns`。`make test` 使用竞态检测；`make lint` 运行 gofmt 和 go vet。

### OpenWrt APK

使用与目标 OpenWrt 版本匹配的 SDK；此仓库的构建脚本针对 x86_64 SDK 生成 APK。将源码和 SDK 放在 Linux x86_64 构建主机上，在项目根目录执行：

```sh
./scripts/build-openwrt.sh /path/to/openwrt-sdk
```

脚本会同步 feed、构建 `updns`、`luci-app-updns`，并按 LuCI 构建规则生成简体中文翻译包。重复构建前如需安装为同版本更新，先提升对应包版本：

- 后端：[`openwrt/updns/Makefile`](../openwrt/updns/Makefile) 中的 `PKG_RELEASE`
- LuCI 与翻译：[`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile) 中的 `PKG_VERSION` 或 `PKG_RELEASE`

生成的 APK 位于 SDK 的 `bin/packages/x86_64/` 下，分别在 `packages/` 和 `luci/` 子目录中。具体文件名以 SDK 输出为准。

APK 未使用受信任密钥签名。将三个包复制到设备后安装：

```sh
apk add --allow-untrusted \
    /tmp/updns-<版本>.apk \
    /tmp/luci-app-updns-<版本>.apk \
    /tmp/luci-i18n-updns-zh-cn-<版本>.apk
```

也可使用仓库的 [`deploy.sh`](../deploy.sh) 上传和安装：

```sh
./deploy.sh ./artifacts/updns-<版本>.apk \
    ./artifacts/luci-app-updns-<版本>.apk \
    ./artifacts/luci-i18n-updns-zh-cn-<版本>.apk \
    root@路由器主机地址
```

测试/开发环境可参考 [`scripts/build-openwrt.sh`](../scripts/build-openwrt.sh) 与部署脚本；正式发布应使用与目标固件匹配的 SDK，并在 OpenWrt 设备上验证。

## 故障排查

- **服务未运行**：检查 `/etc/config/updns` 中 `general.enabled`，查看 `logread -e updns`，并确认监听端口未被占用。
- **上游查询失败**：检查上游地址、协议、网络连通性和设备 CA 证书；将 `log_level` 调为 `debug` 获取更多信息。
- **缓存未生效**：确认 `cache_enabled=1`；NXDOMAIN 规则响应不查询上游 但 缓存;0.0.0.0 规则响应不查询上游 但 缓存。
- **nftables 更新失败**：确认 `nftables-json` 已安装、目标集合已创建，且集合族与地址族匹配。
- **LuCI 无法读取数据**：核对 rpcd ACL 是否包含所需的 UCI、文件或命令权限，并重载 rpcd/LuCI 服务。

UpDNS 的常规日志由 procd 收集，可通过 `logread` 查看。DNS 查询逐条日志需同时启用 `log_query`。

已胜出的有效 DNS 响应（包括上游返回的 `SERVFAIL`、`REFUSED` 等错误码及无 SOA 的否定响应）不作为查询失败告警，启用 `log_query` 时按实际 RCODE 记录常规逐条查询日志。仅当所有上游均未获得有效最终响应（超时、问题不匹配、referral、截断、CNAME 补查失败等）时，才按现有失败日志策略记录 warn，日志包含各上游的具体失败原因。

开启“记录查询日志”并在 dnsmasq 配置 `add_subnet` 后，日志可记录 `client` 与 `ecs`。

## 项目结构

| 区域 | 位置 | 用途 |
|---|---|---|
| 程序入口与 CLI | `cmd/updns/` | 服务启动、查询命令和输出 |
| 配置、规则、上游、缓存 | `config/`、`rules/`、`resolver/`、`cache/` | UCI 配置解析、匹配、DNS 查询及缓存 |
| DNS 服务与控制接口 | `server/` | 请求处理、控制接口、状态和缓存逻辑 |
| nftables 与日志 | `nftset/`、`logging/` | 集合操作和日志 |
| OpenWrt 包和 LuCI | `openwrt/` | init 脚本、UCI 默认配置、界面、ACL 和翻译 |
| 构建与部署 | `scripts/build-openwrt.sh`、`deploy.sh`、`Makefile` | 本机构建、SDK 打包和设备安装 |

## 许可证

[Apache License 2.0](../LICENSE)
