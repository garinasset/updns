# 道岔 DNS 指南

**简体中文** | [English](./GUIDE.en.md)

[← 返回 README](../README.md) · [项目仓库](https://github.com/garinasset/updns)

本文面向道岔 DNS（Turnout DNS，应用名：`updns`）用户和维护者，集中说明产品特点、配置、技术细节、构建流程以及 LuCI 页面与后端配置的对应关系。

## 概览

updns 是一个`OpenWrt`上的本地 DNS 服务器，与`dnsmasq`不同的是，`updns`提供了更灵活的 DNS 路由、 `NFT set`联动，除此之外它还支持 `DoT`、`DoH` 且拥有强大的 `广告拦截` 和 `DNS 审计` 能力。

在你的`OpenWrt`网络，`updns`既可以被作为独立的本地 DNS 服务器，也可以被作为`dnsmasq`的上游。

### 主要能力

- **转发**：动态调度、严格顺序和并发查询；
- **协议**：UDP、TCP、[![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858) 和 [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484);
- **缓存**：容量限制、主动预取（在缓存记录 TTL 到期前主动刷新记录）;
- **日志**：解析日志、系统日志；
- **规则**：域名匹配、广告拦截（Zero-IP 或 NXDOMAIN）、DNS 路由、`NFT set`联动、地址族过滤;
- **生态**：[![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) 深度集成，支持 UCI 配置、RPC/ubus 接口、本地解析文件 `resolv.conf.auto` 读取、nftables 集成及   [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) Web 管理。
- **DNS 审计**：CLI 命令行 `updns <域名>` 审计 DNS 解析传导路径, 包括命中规则、缓存/上游路径、 nftables 集合及解析结果。

## 配置与技术细节

### OpenWrt 配置使用 UCI 格式：

- 主配置：`/etc/config/updns`
- 域名列表：`/etc/updns/domainlists/`，每行一个域名
- 默认端口：`153`，同时监听 UDP 和 TCP

软件包默认配置五个上游 DNS：Tencent-UDP、Google-DoH、Cloudflare-DoH、Tencent-DoH 和 AdGuard-DoH。
- “默认上游”仅指随包配置文件中预先配置的上游，仅供快速设置使用, 可修改或删除，删除全部也不会自动补回。

### 设置及端口

LuCI 界面中「设置及端口」选项卡, 用以配置 updns 的监听端口及相关设置。

#### 1.「启用」

LuCI 界面的「启用」开关, 用来启动或停止 updns 服务, 对应配置中的 `general.enabled`。

- updns 服务由 OpenWrt init/procd 管理。配置中的 `general.enabled` 默认是 `0`, 保存并应用配置时会触发 init 脚本重新加载，并按该字段启动或停止服务进程。

- 新安装默认 `general.enabled=0`，服务进程不会启动，开机自启也处于禁用状态。在 LuCI 中启用并保存应用后，会启用开机自启并启动服务；关闭并保存应用后，会停止服务并禁用开机自启。升级时保留已有配置，并按其启用状态处理服务。

- 通过执行 `/etc/init.d/updns` 脚本的各个命令，可以手动控制 updns 服务的启停及状态查询。
    ```sh
    /etc/init.d/updns enable
    /etc/init.d/updns disable
    /etc/init.d/updns start
    /etc/init.d/updns stop
    /etc/init.d/updns restart
    /etc/init.d/updns reload
    /etc/init.d/updns status
    ```
    - 执行 `enable` 仅启用开机启动，不立即启动进程，也不修改 `general.enabled`；

    - 执行 `disable` 仅禁用开机启动，不立即停止进程，也不修改 `general.enabled`；

    - 执行 `start` 仅在 `general.enabled=1` 时启动服务进程；若进程已在运行，则不重复启动；

    - 执行 `restart` 等同于先 `stop`（停止进程并清理 nftables set）再 `start`；缓存随服务进程重启而重建。

    - 执行 `reload`
        - 在 `general.enabled=1` 时启用开机自启并走重启路径；
        - 在 `general.enabled=0` 时走停止路径并禁用开机自启；

    - 执行 `stop` 会停止服务进程，但不修改 `general.enabled` 或开机自启状态；退出后进程内的缓存不再存在，其内存由操作系统回收。随后清理当前配置所引用的 nftables set；

- LuCI 保存并应用配置会触发 `reload`。

#### 2.「端口」

LuCI 界面的「端口」文本框, 用以配置 updns 的接收 DNS 查询的 UDP 和 TCP 端口。默认端口为 `153`。

#### 3.「使用本地解析文件」

LuCI 界面的「使用本地解析文件」开关, 用以控制是否启用本地解析文件, 对应配置文件 `use_local_resolv` 字段, 默认启用。

- 启用时，所指定本地解析文件（默认 `/tmp/resolv.conf.d/resolv.conf.auto`）中的 nameserver 会以 UDP 上游形式自动添加到列表顶部，按文件顺序排列，并跳过重复地址及已由自定义上游配置的相同地址。服务运行时也会定期检查文件并更新导入的上游；文件不存在时不导入，读取错误会显式报告。

#### 4.「上游 DNS 选择策略」

LuCI 界面的「上游 DNS 选择策略」下拉框对应 `query_strategy` 字段，定义当前候选上游组的查询机制；候选组来自命中规则，或无规则命中时的完整上游列表。

- `query_strategy` 仅接受 `0`、`1`、`2`，默认 `2`（并发查询）；

    0. **动态调度（0）**：同一个有序上游组初始上游平权并发查询，首个有效最终响应胜出，记录赢家自身请求耗时 `t`。后续通常只查询该上游；等待达到 `min(2t, 该上游配置超时)` 时，保留原请求并同时追加其他上游，所有在途请求中首个有效完成者胜出。原上游提前传输失败或响应无效时立即追加其他上游。只有竞速胜出才更新优选及 `t`，普通单上游成功不更新。追加上游的耗时从自身请求启动计算，不包含之前的等待；原优选仍有先启动优势。没有固定次数、定时重评或独立后台探测；原优选持续正常时不会主动发现其他上游变快，不保证始终延迟最低。

    1. **严格顺序（1）**：按引用顺序逐个尝试，直到获得有效最终响应。

    2. **并发查询（2）**：并发请求所选上游，以首个有效最终响应胜出，取消其他在途请求。

- 三种策略统一适用于 UDP/TCP/DoT/DoH，动态调度采用响应延迟触发的追加竞速机制。上游配置变化和清空缓存会重置优选及延迟基线状态。

- 有效缓存直接返回，不产生上游查询。过期刷新和主动预取优先使用该条目原胜出上游，不应用 `2t` 追加竞速；仅当原上游不可用、达到配置超时或响应无效时，按所选策略尝试当前组其他上游，成功后更新条目路径。规则或服务绑定发生变化时重新校验路由，不固守旧路径。没有匹配规则且没有兜底规则时，以整个当前上游列表作为候选组，包括启用且成功导入的本地 resolver 上游。本地 resolver 在列表顺序上靠前，实际查询仍遵循所选策略，不保证优先胜出；有效 HTTPS/SVCB 目标名绑定按绑定规则路由。

- 三种策略遵循相同的响应判定与缓存规则：

    - **响应有效性：** 响应须匹配请求的 ID、操作码、问题域名（不区分大小写）、类型和类，并设置响应标志。有效性不受 RCODE 限制，`NOERROR`、`NXDOMAIN`、`FORMERR`、`SERVFAIL`、`REFUSED`、`NOTIMP` 等有效响应均可胜出。错误码响应也必须包含且匹配原始 Question；省略时不能胜出，失败诊断保留实际 RCODE 和 Question 数量。
    - **CNAME 处理：** `NOERROR` 响应须包含所查类型记录，或完整的 CNAME 链及其终点结果。遇到未完成的 CNAME 链，在同一上游补查链终点，沿用原规则、超时预算和取消信号，不重新匹配规则。最多允许 16 跳，并检测跨响应循环。仅含 NS 委派、无 SOA 的 referral 不是终点结果；`NXDOMAIN` 不得包含所查类型的终点记录。
    - **缓存资格：** 响应胜出后独立判断缓存资格，不影响胜出及客户端返回。正向记录按 TTL 缓存；NODATA 和 NXDOMAIN 须包含覆盖终点名称的有效 SOA，方可进行负缓存。缺少 SOA 或不符合缓存规范的响应仍正常返回，但不写入缓存。错误码响应不缓存，其中的地址也不写入 nftables set。
    - **并发查询：** 首个有效最终响应胜出后，立即取消其他上游请求：UDP/TCP/DoT 关闭在途连接，DoH 取消 HTTP 请求。已经发到远端的请求无法撤回。
    - **截断处理：** UDP 返回 `TC=1` 时，使用同一上游地址和端口进行 TCP 补查，共用超时预算；补查失败或仍然截断时不能胜出。TCP、DoT、DoH 的截断响应也不直接使用。
    - **失败处理：** 所有上游均未获得有效最终响应时，普通查询返回本地生成的 `SERVFAIL`。HTTPS/SVCB 使用 5 秒整体查询预算，失败后返回空答复并抑制重复失败查询 5 分钟；PTR 也使用失败抑制。普通查询失败按现有策略记录限频 warn；HTTPS/SVCB、PTR 及网络未就绪的失败使用 debug 日志。已胜出的有效 DNS 响应不进入失败处理。

### 上游 DNS

LuCI 界面的「上游 DNS」选项卡用于配置可用的上游 DNS 服务器，对应 UCI 中的 `upstream` 配置段。

- 软件包默认配置五个上游 DNS：Tencent-UDP、Google-DoH、Cloudflare-DoH、Tencent-DoH 和 AdGuard-DoH。
    - “默认上游”仅指随包配置文件中预先配置的上游，仅供快速设置使用, 可修改或删除，删除全部也不会自动补回。

- UDP/TCP 默认端口为 53，DoT 为 853，HTTPS/DoH 为 443。自定义 DNS 端口使用“主机:端口”格式（IPv6 示例：[2001:db8::1]:5353）。DoH 请填写完整 URL，例如 https://dns.example:8443/dns-query。

### 域名列表

LuCI 界面的「域名列表」选项卡对应 UCI 中的 `domainlist` 配置段，使用 `name` 和 `file` 字段。列表项由唯一名称和文件路径组成；规则通过列表名称引用文件。

- LuCI 的域名列表中可使用每行操作栏的排序按钮拖动调整顺序，点击“保存”或“保存并应用”后持久保存。域名列表排序不改变规则引用或规则匹配优先级；匹配优先级仍由规则列表的顺序决定。

- 列表文件格式为每行一个域名。匹配不区分大小写，空行和以 `#` 开头的行会忽略。

- 域名匹配时会匹配精确域名及其子域名:
    - `example.com` 可以匹配 `example.com` 和 `www.example.com`，但不能匹配 `superexample.com`；
    - 通过在开头添加 `*` 可以改变这一行为：`*example.com` 可以匹配 `example.com`、`www.example.com`，以及 `superexample.com`。

- 每份列表加载时构建压缩反向 Trie 索引，查询按域名后缀查找，不逐条扫描列表；规则仍按配置顺序评估，不额外缓存匹配结果。

- 单次查询复用域名自身的规则匹配结果：拦截规则直接应答，本地解析、缓存和上游路径不重复扫描规则。HTTPS/SVCB 动态绑定仍在必要位置重新检查，包括异步解析开始及返回前；新查询和后台预取各自匹配，不跨请求缓存路由。

### 缓存
LuCI 界面的「缓存」选项卡, 用以配置 DNS 缓存相关设置。

#### 1.「启用缓存」
LuCI 界面的「启用缓存」开关用于启用或禁用 updns 缓存，对应配置文件 `cache_enabled` 字段，默认启用。禁用缓存时，主动预取也不会运行。
- 正向缓存按响应记录的 TTL 管理；负向缓存使用响应 SOA 提供的 RFC 2308 TTL。缓存命中返回递减后的 TTL，NODATA 和 NXDOMAIN 须包含覆盖终点名称的有效 SOA，方可进行负缓存。缺少 SOA 或不符合缓存规范的响应仍正常返回，但不写入缓存。错误码响应不缓存。

- 收到有效的 NXDOMAIN / NODATA 后，先将响应 SOA 记录自身的 TTL 归一为 `min(SOA TTL, SOA.MINIMUM)`，首次下游响应与入缓存的 SOA 均使用该值；缓存命中按存储经过时间继续递减，不会因读取而重置 TTL。CNAME 链较短时可以使整包缓存提前到期，但不会改变 SOA.MINIMUM 字段；正向答复和直接查询 SOA 记录不做负缓存 TTL 归一化。

- 有效不等于可缓存：上游原始 TTL 为 0、无 SOA 的 NOERROR/NXDOMAIN 及错误码响应均不入缓存；这些胜出响应仍交付等待者，保留原 RCODE。正常负缓存使用 `min(SOA TTL, SOA.MINIMUM)`；CNAME 链与终点否定结果共同缓存时，有效期还受 CNAME TTL 限制。

- HitCount

    - 缓存键为小写完整域名与查询类型，A、AAAA 独立缓存。条目 HitCount 初始为 0，每次有效缓存命中加 1（包括 CLI）；首次上游查询、预取及等待预取不增加命中数。更新条目继承当前版本的全部命中数，包括刷新期间收到的命中；计数饱和递增，不回绕、不做时间衰减。

    - 不再保存删除或淘汰后的计数历史。只有仍存在的条目在刷新替换时继承 HitCount；删除、容量淘汰后重新添加同键条目从 0 开始。

- 添加、删除与生命周期

    - 不运行定时过期清理。新增条目容量不足时只淘汰一个 HitCount 最低的条目，不优先回收过期条目；同计数时淘汰最久未被客户端命中（未命中过则按首次添加顺序）的条目。后台刷新不会提升此同频次优先级。因此，高命中的过期条目可能保留而淘汰有效冷条目。

    - TTL 到期不立即删除，也不允许返回旧数据：保留条目用于调度预取和继承命中数; 主动预取关闭时，过期缓存不会直接返回给客户端；客户端查询会触发重新解析;程序会取出旧缓存记录中的上游作为优先上游，前提是路由规则没变且该上游仍在当前上游列表中；若查询失败，会按当前策略回退到其他上游。关闭主动预取时，过期条目可保留到容量回收或下一次请求重新解析；

    - 缓存数量表示总占用，包含过期条目，不等于可直接应答的数量。

    - 缓存仅驻留内存，不跨进程持久化。显式清空会移除缓存条目并使在途任务的结果失效；停止会等待服务任务结束并清空缓存；重启及 OpenWrt 配置重载（启用时实际走重启路径）会销毁旧进程的缓存，并由新进程从空缓存开始。条目版本标记和清空代次标记仅用于同一进程内的并发保护：旧任务不能删除或覆盖更新后的条目，也不能重新写入已淘汰或已清空的条目；它们不是缓存数据版本，不用于持久化或恢复缓存。清空会解除缓存结构对条目的引用，但尚未结束的在途任务可能暂时持有相关对象；对象不再被引用后才可由 Go 垃圾回收。垃圾回收不保证立即将堆内存归还操作系统，因此进程仍运行时，清空不等于进程占用内存立即下降；服务进程退出后，其内存由操作系统回收。

#### 2.「缓存大小」

LuCI 界面的「缓存大小」文本框, 用以配置 DNS 缓存的最大条目数, 对应配置文件 `cache_size` 字段, 默认值为 2048。

#### 3.「主动预取」
LuCI 界面的「主动预取」开关, 用以启用或禁用 DNS 条目的主动预取, 对应配置文件 `cache_prefetch` 字段, 默认启用。

- 仅使用 `cache_prefetch`，不兼容旧的 `active_preload` 字段，也不自动迁移旧值；缺少新字段时使用默认值 `1`。

- 每 100 毫秒检查调度队列，已消耗至少 80% 原 TTL（剩余有效期不大于 20%）的条目进入待预取池，包含剩余 TTL 归零的旧条目。待预取池不含已在途任务；按原过期时间从早到晚启动。后台同时在途任务上限为 `max(16, min(ceil(cache_size / 128), 64))`，其中 `cache_size` 是配置的缓存容量；默认 2048 条时后台同时在途任务上限为 16。每轮补足空闲名额，慢任务不阻塞下一轮扫描。所有条目均可预取，不以 HitCount 或当前访问量过滤。

- 刷新会先直接查询缓存条目上次胜出的上游。刷新成功就采用它的结果；如果该上游查询失败，再按当前上游 DNS 选择策略尝试其他上游。

- 启用「主动预取」时, 前台请求的响应行为

    - 未过期：立即返回递减 TTL 的缓存，路径为 `Cache Hit`。

    - 已过期且正在预取：等待同一个任务完成，路径为 `Prefetch Hit`；成功返回新答复，失败返回失败，绝不返回过期缓存。日志及 CLI 保留此路径。该名称表示复用预取结果，不计作缓存命中。

    - 已过期但尚未预取，或完全未缓存：由前台立即启动解析，路径为 `Upstream Query`；不受后台同时在途任务上限限制。相同键的其他前台请求及后台扫描复用该在途任务，不重复查询。

- 刷新成功且 TTL 大于 0 时替换对应旧版本并继承 HitCount。刷新失败、TTL 为 0 或胜出响应不可缓存（如错误码、无 SOA 的否定响应）时：旧条目未过期则保留答复、原过期时间、原路径及计数，推迟到原过期时刻再预取，避免每 100ms 重试；旧条目已过期则删除，不返回过期答复、不保存计数历史，也不再后台重试该名称。后续客户端可重新解析，重新缓存后的 HitCount 从 0 开始。

- 有效不等于可缓存：上游原始 TTL 为 0、无 SOA 的 NOERROR/NXDOMAIN 及错误码响应均不入缓存、不进入周期预取；这些胜出响应仍交付等待者，保留原 RCODE。正常负缓存使用 `min(SOA TTL, SOA.MINIMUM)`；CNAME 链与终点否定结果共同缓存时，有效期还受 CNAME TTL 限制。

- 后台任务数不等于网络请求数：前台查询、上游并发及 CNAME/TCP 补查可能增加实际请求数；100 毫秒也不是完成时限。

- 在途结果为各等待者独立复制，避免并发修改同一 DNS 报文。网络 I/O 不持有缓存或调度锁。清空会取消已有任务并唤醒等待者；停止服务统一取消并等待调度循环和所有解析任务退出。

### 日志记录
LuCI 界面的「日志记录」选项卡用于配置 updns 的日志记录行为，对应配置文件 `log_query`、`log_level` 字段。

#### 1.「记录查询日志」
LuCI 界面的「记录查询日志」开关, 用以启用或禁用将每条 DNS 查询写入系统日志, 对应配置文件 `log_query` 字段, 默认禁用。

- updns 日志与 CLI 只反映实际到达 updns 的查询，未到达 updns 的查询不会出现在其中。

- 对于通过 dnsmasq 转发的查询，只有实际到达 updns 的请求才会被记录。

#### 2.「日志级别」
LuCI 界面的「日志级别」下拉框, 用以设置 updns 服务进程的日志输出级别, 对应配置文件 `log_level` 字段, 可选值包括 `error`、`warn`、`info`、`debug`，默认值为 `info`。

### 规则

LuCI 界面的「规则」区域用于配置域名匹配、广告拦截、DNS 路由、nftables set 联动和地址族过滤，对应 UCI 中的 `rule` 配置段。

- 规则按顺序评估，命中第一条适用规则即停止。一个规则可引用多个域名列表，命中其中任一个列表即满足域名条件。LuCI 支持拖动普通规则排序；

- 在规则的“如果域名匹配”下拉框中选择“不执行匹配（兜底规则）”（对应 `domain_match='0'`），该规则将被视为兜底规则，兜底规则最多一条且总是在普通规则之后执行。没有兜底规则且请求未命中普通规则时，以整个当前上游列表作为候选组；本地 resolver 按解析文件顺序排在自定义上游之前，自定义上游按配置顺序排列。仅严格顺序策略按此顺序逐个尝试；动态调度和并发查询不保证本地 resolver 优先胜出。本地记录、有效缓存及 HTTPS/SVCB 绑定仍按相应机制处理。

- 命中普通规则或兜底规则后，只使用该规则引用的上游；上游为空或全部失败时，不再自动改用整个上游列表。HTTPS/SVCB 有效绑定可按绑定规则选择上游。有效 DNS 错误码响应也可胜出，不等同于传输失败。

- 规则（`nxdomain`、`zeroip` 除外）可选择一个或多个上游 DNS，并按需指定用于存储 IPv4、IPv6 解析结果的 nftables set，实现 DNS 解析结果与 NFT Set 联动。服务进程重启或重新加载配置时，相关集合会被清空，并根据规则重新动态写入解析结果。

- 规则还可将其引用的上游 DNS 端点 IP 地址加入对应的 nftables set。对于使用主机名指定的端点（包括 DoH URL 中的主机名），updns 会自动解析并定期刷新其 IP 地址。新解析出的地址会追加到集合中，但不会自动从共享集合中移除旧地址。此功能可为透明代理或路由规则提供匹配条件；updns 不会自行配置代理或流量路由，也不能单独保证避免 DNS 泄漏。端点主机名解析使用系统 resolver，需结合系统 DNS、路由及防火墙配置评估实际解析路径。

    | 操作 | 行为 |
    |---|---|
    | `ipv4+ipv6` | 返回 IPv4 和 IPv6 结果 |
    | `ipv4only` | 仅返回 IPv4 结果 |
    | `ipv6only` | 仅返回 IPv6 结果 |
    | `nxdomain` | 直接返回 NXDOMAIN，不查询上游，不缓存该响应 |
    | `zeroip` | Zero-IP 拦截：A 查询返回 `0.0.0.0`，AAAA 查询返回 `::`，不查询上游，不缓存该响应 |

- `nxdomain` 与 `zeroip` 规则应答绕过 updns 缓存读写和周期预取，且不写入 nftables set；应答内容及 TTL 语义: 零地址记录 TTL 为 60 秒，NXDOMAIN 不附加 SOA。CLI/日志始终显示 `NXDOMAIN RULE` 或 `ZERO-IP RULE`，而非缓存命中。上游返回的 NXDOMAIN 仍按原有负缓存规则处理。

- `ipv4+ipv6`、`ipv4only`、`ipv6only` 只过滤 A/AAAA 记录；MX、TXT、NS、SOA、SRV、PTR 等非地址记录及 CNAME 原样返回。HTTPS/SVCB 记录照常返回，`alpn`、`ech` 等参数保留。

- HTTPS/SVCB 处理规则：

    - **地址提示：** `ipv4hint`/`ipv6hint` 按规则动作过滤（如 `ipv4only` 移除 `ipv6hint`），保留的提示地址与 A/AAAA 一样写入规则的 nftables set。

    - **目标名绑定：** 响应中的目标主机名（TargetName，ServiceMode 为 `.` 时即查询名本身）绑定到产生该响应的规则。客户端随后查询目标名时，使用该规则的上游和动作，并将地址同时写入所有绑定规则及目标名自身匹配规则的 set，确保 nftables 路由链一致。多个规则绑定同一目标时，上游和动作取首个绑定规则。

    - **绑定生命周期：** 绑定按记录 TTL 过期，最短 30 秒；容量与 `cache_size` 相同，已满时丢弃新的 HTTPS/SVCB 记录，不淘汰有效绑定。绑定后异步预取目标名地址，不同步追加查询。

    - **限制：** 目标名自身命中 `nxdomain`/`zeroip` 规则，或链长（含 CNAME）超过 16 跳时，丢弃该 HTTPS/SVCB 记录，避免绕过拦截。未命中规则（使用默认上游）的查询不建立绑定；错误码响应原样返回，但不建立绑定，提示地址也不写入 NFT Set。

### DNS 审计 - CLI

```text
root@OpenWrt:~# updns google.com

╭─ updns ──────────────────────────────────────────╮
│
│  DOMAIN
│  ──────────────────────────────────────────────
│  Domain       : google.com.
│  Query Type   : A / AAAA
│  Query Path   : Cache Hit
│
│  DNS RESULT
│  ──────────────────────────────────────────────
│  Matched Rule : US Access
│  Upstream     : A=Google-DoH / AAAA=Google-DoH
│  Action       : IPv4 + IPv6
│  Response     : NOERROR
│
│  NFT SETS
│  ──────────────────────────────────────────────
│  IPv4         : inet/fw4/us4
│                 ✓ Contains resolved IPv4
│  IPv6         : inet/fw4/us6
│                 ✓ Contains resolved IPv6
│
│  ANSWERS
│  ──────────────────────────────────────────────
│  • 142.251.219.142
│  • 2607:f8b0:4005:818::200e
│
╰──────────────────────────────────────────────────╯
```

- 服务运行时，CLI 通过本机控制接口查询，使用服务的规则、缓存并显示 nftables 集合信息；服务未运行时会独立查询，不使用服务缓存。查询默认分别执行 A 和 AAAA 查询。

- `Query Path` 的 `Local Answer` 表示直接来自本地 hosts/DHCP；`Cache Hit` 表示有效缓存；`Prefetch Hit` 表示等待并复用预取任务；`Upstream Query` 表示前台解析。`Upstream` 按类型分别显示，例如 `A=local-dhcp / AAAA=local-hosts`；无来源时显示 `(none)`。缓存命中显示缓存原来源，不表示本次访问了上游。本地 ANY 答复同时包含两种来源时显示 `local-hosts+local-dhcp`。

- 查询失败时仍会输出完整报告：`Response` 显示 `ERROR`，并新增 `ERRORS` 分区，按查询类型给出规范化的简短原因（如 `Upstream timeout`、`Upstream connection refused`、`Upstream host unresolvable`、`Network not ready`、`No upstream configured`、`All upstreams failed`），命令以非 0 退出码结束。仅 A 或 AAAA 之一失败时，另一类结果照常显示，只要有一类成功退出码即为 0。

- 边框使用终端实际列数，不限制最小或最大宽度；无法读取终端尺寸时使用 `COLUMNS`，仍不可用则使用 48 列。文本按 Unicode 显示宽度对齐、按完整字素换行，长值续行对齐值起点；窄终端改为字段名和值分行。极窄终端会缩减缩进，无法容纳的双列字素显示为 `?`。动态文本中的 ANSI 颜色/链接转义被移除，控制字符替换为空格。部分终端对 emoji 和歧义宽度字符的显示仍可能不同。

## 深度技术细节

### 本地解析

updns 直接读取 `/etc/hosts`、`/tmp/hosts/` 下的 hosts 文件和 DHCP 租约，支持 A、AAAA、PTR 和 ANY。本地域名及路径取自 `/etc/config/dhcp` 的第一个 dnsmasq 配置段：`domain`、`expandhosts`、`leasefile`、`addnhosts` 和 `hostsdir`；也读取 odhcpd 的 `hostsdir`。无 OpenWrt DHCP 配置时，默认租约路径为 `/tmp/dhcp.leases`、域名后缀为 `lan`，不自动扩展 hosts 短名称。

- 本地记录优先于旧的上游缓存，hosts 仅覆盖同名、同地址族的 DHCP 租约：例如 hosts 提供 IPv6 时，仍可使用 DHCP 的 IPv4；租约到期不再使用。
- 文件、目录和本地域名设置在查询时检测更新，解析结果使用 TTL 0，不写入 DNS 缓存、不做周期预取；日志和 CLI 显示 `Local Answer`。

- 本地记录不绕过 NXDOMAIN/Zero-IP 拦截，也遵守地址族过滤和 nftset 规则。

- 本地数据文件无法读取或解析时日志记录限频告警，并继续使用缓存和上游解析。`use_local_resolv` 只控制从 resolv.conf 导入上游地址，与本地记录解析无关。


### 数据结构与性能

缓存采用哈希表与三个索引最小堆实现，分别用于管理缓存项的 LFU 淘汰顺序、缓存刷新调度时刻，以及已就绪刷新任务的过期时间。命中优先级更新、添加/删除和单条调度为 O(log N)，空闲扫描为 O(1)；到期条目仅在状态转换时进入就绪队列，避免每 100 毫秒全量遍历、排序和复制 DNS 记录。所有索引随删除同步回收，无无界失效节点积累；总体空间 O(N)，在途任务表另按实际共享解析数计。

### GitHub Actions 软件源

仓库提供 GitHub Actions 和 GitHub Pages 自动发布流程。当前配置使用 **OpenWrt 25.12.6 SDK**，软件源按发行系列发布在 **25.12** 目录中，为 OpenWrt 目标定义中 Go 支持的包架构分别构建后端。LuCI 和中文翻译是架构无关包，仅使用 x86_64 SDK 构建一次，再将相同文件复制到每个受支持架构的软件源目录，并校验其 `noarch` 元数据。所有架构均构建成功后才发布；实际可用架构以工作流结果为准，其他 OpenWrt 发行系列须先验证并调整流程，不能视为已支持。同系列共用软件源仍须验证补丁版本间的依赖和 ABI 兼容性；构建成功不等同于真实设备测试。

维护者首次使用时：

1. 私钥文件内容添加为仓库 Actions secret，名称为 `APK_SIGNING_KEY`。更换密钥需要用户重新建立信任；流程会拒绝直接替换已有签名密钥。

2. 工作流使用名为 `downloads.garinasset.com` 的 GitHub Environment，仓库首页 **Deployments** 将按此名称展示部署。

3. 默认分支推送或手动运行时，流程对比已发布的包版本及 SDK 版本：后端检查 `openwrt/updns/Makefile`，LuCI 和翻译检查 `openwrt/luci-app-updns/Makefile` 中的 `PKG_VERSION`、`PKG_RELEASE`。首次发布会构建全部包；包版本和 SDK 均未变化时只运行测试，不更新软件源；SDK 未变化且只更新其中一组时，发布时另一组保留原有包。变更后的包版本必须高于已发布版本，也不需要 Git 标签。中文翻译包版本包含 LuCI 的 release，因而增大 LuCI 的 `PKG_RELEASE` 也会更新翻译包。

4. 调整工作流的 `OPENWRT_RELEASE` 时，使用完整 SDK 版本，例如 `25.12.6`；脚本自动提取系列 `25.12`。同系列 SDK 更新会触发全部包重新构建、发布，即使包版本未变；但客户端不会将同版本重建视为常规升级，如需用户通过 `apk upgrade` 获取新构建，也应增加对应包的 `PKG_RELEASE`。同系列目录只保留最新一套包，不保留已替换的旧包；切换到新系列时建立新目录，并保留已发布的其他系列软件源及其签名。同系列内亦应保持对旧补丁版本的兼容，否则不能直接覆盖；请注意 GitHub Pages 的容量限制。各系列的 `manifest.json` 记录完整 SDK 版本、包版本及包的 SHA-256。

5. 下载入口为 <https://downloads.garinasset.com/updns/>。CI 为项目根目录和每层子目录生成静态目录索引，展示文件大小（字节）、SHA-256 和下载链接；不另行生成每个目录的校验清单文件。页面复用下载站首页的 CSS、字体、图标与明暗主题设置，显示 `Home / updns / openwrt` 形式的面包屑；首行 `..` 返回上一级，项目根目录返回下载站首页。所有字段完整显示，列内空间足够时不换行、不足时自动换行。模板位于 `scripts/pages.html`；只有模板变化时，CI 跳过 APK 构建、SDK 和构建产物下载，通过 `update-pages` 从已发布站点恢复所有系列的软件源文件，重新生成 HTML 并整站部署，不依赖 Actions 临时产物。包版本或 SDK 变化时仍执行完整构建流程。恢复时校验签名、公钥与每个 APK 的 SHA-256；新清单还记录 `packages.adb` 的 SHA-256，旧清单缺少该字段时会打印警告，原索引保持不变。页面模板摘要单独记录在签名的 `openwrt/pages.json` 中，纯页面更新不修改包清单及其签名。签名密钥仍须配置，用于确认公钥并签名页面状态。字体、图标和主页 CSS 需要浏览器联网加载。这不是 GitHub Pages 服务器的自动目录列表，也不会修改域名根首页。同站点哈希不能替代公钥的独立可信验证或软件源签名。

6. 此远程软件源与本地 `artifacts/` 目录互不影响。

## 基本 UCI 示例

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option query_strategy '2'
    option cache_enabled '1'
    option cache_size '2048'
    option cache_prefetch '1'
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

页面入口为 **服务 → 道岔 DNS**，路由为 `/admin/services/updns`。当前页面分为运行状态、设置及端口、上游 DNS、域名列表、缓存、日志和规则等区域。

### 页面与配置模型

| 页面区域 | UCI section / 字段 | 产品行为 |
|---|---|---|
| 运行状态 | 服务状态、缓存状态文件 | 展示服务状态和缓存条目数，并定期刷新 |
| 设置及端口 | `updns.general`：`enabled`、`port`、`use_local_resolv`、`local_resolv_file`、`query_strategy` | 启停服务、监听端口、本地 resolver 和上游 DNS 选择策略 |
| 上游 DNS | `updns.upstream`：`name`、`address`、`protocol`、`timeout` | 管理 UDP/TCP/DoT/DoH 上游；可拖动调整自定义上游顺序，本地 resolver 自动生成项固定在顶部且不可编辑、删除或拖动 |
| 域名列表 | `updns.domainlist`：`name`、`file` | 上传或管理列表文件，供规则引用 |
| 缓存 | `updns.general`：`cache_enabled`、`cache_size`、`cache_prefetch` | 控制缓存、容量和主动预取 |
| 日志 | `updns.general`：`log_query`、`log_level` | 控制查询日志和日志级别 |
| 规则 | `updns.rule`：`name`、`domain_match`、`domain_lists`、`upstreams`、`action`、`ipv4_nftsets`、`ipv6_nftsets`、`add_upstream_ips` | 设置匹配条件、上游、返回类型及 nftables 处理 |

新安装默认 `enabled=0`，服务进程不启动，开机自启禁用；默认端口 `153`、使用本地 resolver、并发查询（`query_strategy=2`）、缓存启用且容量为 `2048`、主动预取启用、查询日志关闭。

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

页面从 UCI 读取和保存配置；本地 resolver 项会显示为自动生成的 `local-resolv-N` 上游。全局上游顺序用于无普通规则及兜底规则命中时的候选组顺序；规则显式选择的上游仍按规则自身列表排序，实际查询遵循所选策略。服务状态来自 rpcd `service.list`，缓存条目数来自 `/tmp/updns_cache_size`，nftables 集合由页面调用 `nft list sets` 获取，并根据集合类型分别筛选 IPv4 与 IPv6 集合。域名列表通过 rpcd 文件接口读写。

自动生成上游与自定义上游使用一致的表格列和右侧拖动、编辑、删除控件；自动生成项的三个控件全部禁用，悬停提示其由系统自动生成。
上游列表保留列标题，仅隐藏列说明行；编辑弹窗仍保留字段标题和帮助说明。列表上方仅显示本地 resolver 的自动加载信息或读取错误，不显示拖动排序说明。
协议列使用线条图标和胶囊标签：UDP 琥珀色、TCP 绿色、DoH 宝蓝色、DoT 深海蓝文字配浅蓝背景。协议文字始终保留，不仅依靠颜色区分；图标为内嵌 SVG，不依赖外部资源。自动生成项与自定义项样式一致，协议配置值和编辑下拉框不变。

修改页面时应同步检查相关 UCI 字段、rpcd ACL 和后端行为。ACL 采用明确的文件路径授权；增加新的状态文件、文件读写路径或系统命令时，需要一并更新 ACL。可见文本使用 `_()` 并同步维护 PO 翻译。若调整表单区域或 Tab 布局，需检查页面中的映射和排序逻辑。


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

### 安装 APK (本地构建)

由于编译生成的 APK 未使用受信任密钥签名, `--allow-untrusted` 参数是必要的。将三个包复制到设备后安装：

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

仓库的 `artifacts/` 目录用于保存最新构建且通过测试的安装包；同一组件的新包验证通过后替换旧包。

### 运行方式

updns 支持两种运行模式：**独立运行** 或 **作为 dnsmasq 的上游**, 你可以按需选择。

- #### 独立运行

    将 updns 作为本地 DNS 服务器，由客户端直接向 updns 发起 DNS 查询。

    ```sh
    uci set dhcp.@dnsmasq[0].port='0'
    uci commit dhcp

    uci set updns.general.enabled='1'
    uci set updns.general.port='53'
    uci commit updns

    /etc/init.d/dnsmasq restart
    /etc/init.d/updns restart
    ```
    - 将 dnsmasq DNS 监听端口设为 0，即关闭其 DNS 解析服务，保留 DHCP 等其他功能；

    - 配置 updns 服务监听 53 端口，直接接收客户端 DNS 查询。请确保没有其他 DNS 服务占用 53 端口。

- #### 作为 dnsmasq 的上游

    将 updns 作为 dnsmasq 的上游 DNS 时，由 dnsmasq 转发客户端的 DNS 查询到 updns。

    ```sh
    uci -q delete dhcp.@dnsmasq[0].port
    uci -q delete dhcp.@dnsmasq[0].server
    uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#153'
    uci set dhcp.@dnsmasq[0].cachesize='0'
    uci set dhcp.@dnsmasq[0].noresolv='1'
    uci set dhcp.@dnsmasq[0].add_subnet='32,128'
    uci commit dhcp

    uci set updns.general.enabled='1'
    uci set updns.general.port='153'
    uci commit updns

    /etc/init.d/updns restart
    /etc/init.d/dnsmasq restart
    ```

    - 建议**只在 updns 中启用缓存**，将 dnsmasq 的缓存大小设置为 `0`（即关闭 dnsmasq 缓存）。这样可以避免 dnsmasq 与 updns 各自缓存带来的状态分散，以及规则或上游变更后旧结果仍留在 dnsmasq 缓存中的情况;禁用 dnsmasq 缓存后，转发到 updns 的查询由 updns 管理缓存。

    - `uci set dhcp.@dnsmasq[0].add_subnet='32,128'` 配置 dnsmasq 在转发 DNS 查询时附带 ECS 信息，将客户端 IPv4、IPv6 地址分别以 /32、/128 的掩码传递给 updns，供其日志记录。该设置传递完整客户端地址，请按隐私需求决定是否启用；它不会改变 updns 的域名规则匹配或上游选择，也不表示 updns 会将客户端 ECS 转发给上游。

## 故障排查

- **服务未运行**：检查 `/etc/config/updns` 中 `general.enabled`，查看 `logread -e updns`，并确认监听端口未被占用。
- **上游查询失败**：检查上游地址、协议、网络连通性和设备 CA 证书；将 `log_level` 调为 `debug` 获取更多信息。
- **缓存未生效**：确认 `cache_enabled=1`；`nxdomain`、`zeroip` 规则直接应答，不查询上游、不读写缓存、不做预取。上游返回的 NXDOMAIN 只有符合负缓存条件时才缓存。
- **nftables 更新失败**：确认 `nftables` 已安装、目标集合已创建，且集合类型与地址族匹配。
- **LuCI 无法读取数据**：核对 rpcd ACL 是否包含所需的 UCI、文件或命令权限，并重载 rpcd/LuCI 服务。

updns 的常规日志由 procd 收集，可通过 `logread` 查看。DNS 查询逐条日志需同时启用 `log_query`。

已胜出的有效 DNS 响应（包括上游返回的 `SERVFAIL`、`REFUSED` 等错误码及无 SOA 的否定响应）不作为查询失败告警，启用 `log_query` 时按实际 RCODE 记录常规逐条查询日志。仅当所有上游均未获得有效最终响应（超时、问题不匹配、referral、截断、CNAME 补查失败等）时，普通查询才按现有失败日志策略记录限频 warn，包含各上游的具体失败原因；HTTPS/SVCB、PTR 及网络未就绪的失败使用 debug 日志。

逐条查询日志使用 `QUERY [CLI]` 或 `QUERY [SRV]` 标识来源，依次显示域名、查询类型、匹配规则、查询路径和 DNS 响应码；未匹配规则时显示 `-`。服务端查询有客户端地址时追加 `@客户端地址`，有 EDNS Client Subnet（ECS）时追加 `#网段`；缺失的字段直接省略，CLI 查询不显示客户端或 ECS 占位符。dnsmasq 可通过配置 `add_subnet` 提供 ECS。

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
