# UpDNS 使用与开发指南

**简体中文** | [English](./GUIDE.en.md)

[← 返回 README](../README.md) · [项目仓库](https://github.com/garinasset/updns)

本文面向 UpDNS 用户和维护者，集中说明产品行为、配置方式、构建流程，以及 LuCI 页面与后端配置的对应关系。

## 产品概览

UpDNS 是运行于 OpenWrt 的 DNS 服务。它通过 UDP/TCP 接收查询，根据域名列表规则选择上游 DNS，并可将解析地址写入指定 nftables set。未命中规则时使用默认上游；也可配置兜底规则。

支持的上游协议：UDP、TCP、DNS over TLS（DoT）和 DNS over HTTPS（DoH）。可单独使用 UpDNS，也可将它配置为 dnsmasq 等 DNS 转发器的上游。

主要能力：

- **规则**：按域名列表匹配，支持顺序查询或并发查询多个上游、地址族过滤、NXDOMAIN 响应和 Zero-IP 拦截（返回 0.0.0.0 / ::）等。
- **缓存**：正向缓存按响应记录的 TTL 管理；负向缓存使用响应 SOA 提供的 RFC 2308 TTL。缓存命中返回递减后的 TTL，支持容量限制及主动预取。
- **主动预取**（默认启用）：后台每 100 毫秒扫描缓存，对已过 75% TTL 但尚未过期的条目重新解析（含规则匹配与 nftables 写入），成功后用新结果覆盖缓存。预取不区分条目是否仍被访问，也不会在刷新失败时返回过期数据；刷新失败的条目将自然过期。
- **nftables**：可分别将解析得到的 IPv4 和 IPv6 地址加入规则指定的集合；也可选择加入规则所引用上游 DNS 端点的 IP 地址。
- **LuCI**：通过 Web 界面管理服务、上游、域名列表、缓存、日志和规则。
- **CLI**：`updns <域名>` 查看查询结果、命中的规则、缓存/上游路径及 nftables 集合状态。

## 配置与规则

OpenWrt 配置使用 UCI 格式：

- 主配置：`/etc/config/updns`
- 域名列表：通常存放于 `/etc/updns/domainlists/`，每行一个域名
- 默认端口：`153`，同时监听 UDP 和 TCP

软件包默认配置五个上游：Tencent-UDP、Google-DoH、Cloudflare-DoH、Tencent-DoH 和 AdGuard-DoH。启用本地 resolver 文件时，文件中的 nameserver 也会作为运行时 UDP 上游；默认文件为 `/tmp/resolv.conf.d/resolv.conf.auto`。

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

规则按顺序评估，命中第一条适用规则即停止。一个规则可引用多个域名列表，命中其中任一个列表即满足域名条件。LuCI 支持拖动普通规则排序；在规则的“如果域名匹配”下拉框中选择“不执行匹配（兜底规则）”（对应 `domain_match='0'`），该规则将被视为兜底规则，兜底规则最多一条且总是在普通规则之后执行。

| 操作 | 行为 |
|---|---|
| `ipv4+ipv6` | 返回 IPv4 和 IPv6 结果 |
| `ipv4only` | 仅返回 IPv4 结果 |
| `ipv6only` | 仅返回 IPv6 结果 |
| `nxdomain` | 直接返回 NXDOMAIN，不查询上游，缓存该响应 |
| `zeroip` | Zero-IP 拦截：A 查询返回 `0.0.0.0`，AAAA 查询返回 `::`，不查询上游，缓存该响应 |

`nxdomain` 与 `zeroip` 响应的缓存 TTL 固定为 60 秒，且不写入 nftables set。

规则（`nxdomain`、`zeroip` 除外）可选择一个或多个上游，并为 IPv4/IPv6 分别指定 nftables set。若启用并发查询，将并发查询所选上游并使用首个有效应答；否则按配置顺序逐个查询，直到成功。没有匹配规则且没有兜底规则时，使用默认上游；启用本地 resolver 时，其上游排在其他默认上游之前。

规则还可选择把其引用上游 DNS 端点的 IP 地址加入对应 nftables set。主机名（包括 DoH URL 主机名）会解析为 IP，并定期刷新；新地址会添加到集合中，不会自动从共享集合删除旧地址。

### 基本 UCI 示例

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option concurrent_query '0'
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
| 设置及端口 | `updns.general`：`enabled`、`port`、`use_local_resolv`、`local_resolv_file`、`concurrent_query` | 启停服务、监听端口、本地 resolver 和上游查询模式 |
| 上游 DNS | `updns.upstream`：`name`、`address`、`protocol`、`timeout` | 管理 UDP/TCP/DoT/DoH 上游 |
| 域名列表 | `updns.domainlist`：`name`、`file` | 上传或管理列表文件，供规则引用 |
| 缓存 | `updns.general`：`cache_enabled`、`cache_size`、`active_preload` | 控制缓存、容量和主动预取 |
| 日志 | `updns.general`：`log_query`、`log_level` | 控制查询日志和日志级别 |
| 规则 | `updns.rule`：`name`、`domain_match`、`domain_lists`、`upstreams`、`action`、`ipv4_nftsets`、`ipv6_nftsets`、`add_upstream_ips` | 设置匹配条件、上游、返回类型及 nftables 处理 |

新安装默认 `enabled=0`，服务不会自动启动；默认端口 `153`、使用本地 resolver、顺序查询、缓存启用且容量为 `4096`、主动预取启用、查询日志关闭。

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
