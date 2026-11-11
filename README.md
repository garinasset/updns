# 道岔 DNS

**简体中文** | [English](./README.en.md)


道岔 DNS（Turnout DNS，应用名：`updns`） 是一个`OpenWrt`上的本地 DNS 服务器，与`dnsmasq`不同的是，`updns`提供了更灵活的 DNS 路由、 `NFT set`联动，除此之外它还支持 `DoT`、`DoH` 且拥有强大的 `广告拦截` 和 `DNS 审计` 能力。

在你的`OpenWrt`网络，`updns`既可以被作为独立的本地 DNS 服务器，也可以被作为`dnsmasq`的上游。


## 🧩 技术栈

| 技术 | 作用 |
| --- | --- |
| [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/) | 后端开发语言，编译为单个可执行文件，实现 DNS 服务、规则匹配与缓存 |
| [![miekg/dns](https://img.shields.io/badge/miekg%2Fdns-00ADD8?logo=go&logoColor=white)](https://github.com/miekg/dns) | Go DNS 库，负责 DNS 报文解析、UDP/TCP 服务与上游查询 |
| [![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858) | 加密的上游 DNS 传输协议，经 TLS 查询上游 |
| [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484) | 基于 HTTPS 的上游 DNS 传输协议 |
| [![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) | 目标运行平台，提供路由器系统、软件包与服务管理 |
| [![APK](https://img.shields.io/badge/APK-OpenWrt%20Packages-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/additional-software/apk) | OpenWrt 25.12 起使用的软件包格式，用于分发后端、LuCI 与中文翻译 |
| [![OpenWrt SDK](https://img.shields.io/badge/OpenWrt%20SDK-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/toolchain/using_the_sdk) | 交叉编译与打包工具链，在 Linux x86_64 上构建 APK |
| [![UCI](https://img.shields.io/badge/UCI-OpenWrt%20Config-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/uci) | OpenWrt 统一配置接口，保存端口、上游、域名列表与规则 |
| [![dnsmasq](https://img.shields.io/badge/dnsmasq-DNS%20Forwarder-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/dhcp) | 可选的 DNS 转发集成：dnsmasq 可将查询转发到 updns |
| [![procd](https://img.shields.io/badge/procd-init%20%26%20process-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/procd-init-scripts) | OpenWrt 进程管理，负责服务启停、常驻与日志收集 |
| [![nftables](https://img.shields.io/badge/nftables-Netfilter-0F4C81)](https://netfilter.org/projects/nftables/) | 可选的 Linux 防火墙集成；按规则配置将适用的解析地址写入 nft set，供路由或过滤使用 |
| [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) | OpenWrt Web 管理界面框架，提供 updns 配置页面与中文翻译 |
| [![rpcd / ubus](https://img.shields.io/badge/rpcd%20%2F%20ubus-OpenWrt%20RPC-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/techref/ubus) | LuCI 与 OpenWrt 的 RPC 接口，用于读取运行状态及所需文件 |
| [![JavaScript](https://img.shields.io/badge/JavaScript-F7DF1E?logo=javascript&logoColor=black)](https://developer.mozilla.org/docs/Web/JavaScript) | LuCI 视图逻辑，实现规则编辑、动态选择与运行状态展示 |
| [![gettext](https://img.shields.io/badge/gettext-PO-A42E2B?logo=gnu&logoColor=white)](https://www.gnu.org/software/gettext/) | 国际化翻译格式，维护简体中文界面文本 |
| [![GitHub](https://img.shields.io/badge/GitHub-181717?logo=github&logoColor=white)](https://github.com/) | 代码托管与版本管理 |
| [![Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-D22128?logo=apache&logoColor=white)](./LICENSE) | 开源许可证 |

## 🚀 工作流程

**客户端 → updns → 规则匹配 → 本地数据 / 缓存 / 上游 DNS（UDP · TCP · DoT · DoH）→ nftset → 返回结果**

updns 根据域名匹配规则，优先处理拦截，再使用 hosts、DHCP 本地数据及有效缓存；需要查询上游时，按选择策略查询规则指定的上游（UDP、TCP、DoT、DoH），默认使用并发查询。没有匹配规则且没有兜底规则时，以整个当前上游列表作为候选组；本地 resolver 仅在列表顺序上靠前，不保证优先胜出。缓存刷新优先复用原胜出上游。所有解析结果均遵循适用的拦截、地址族及 nftset 规则。

## 💻 DNS 审计 - CLI 输出示例

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

## 🖥️ LuCI 支持

安装 updns 与 LuCI 应用后，在 OpenWrt 管理界面进入 **服务 → 道岔 DNS** 即可配置使用。

- 运行状态

- 配置
    - 设置及端口
    - 上游 DNS
    - 域名列表
    - 缓存
    - 日志记录

- 规则


## ⚙️ 运行方式

updns 支持两种运行模式：**独立运行** 或 **作为 dnsmasq 的上游**, 你可以按需选择。

- ### 独立运行

    将 updns 作为局域网 DNS 服务器，由客户端直接向 updns 发起 DNS 查询。

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

- ### 作为`dnsmasq`的上游

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

## 📖 详细文档

完整的功能说明、编译安装、配置、CLI 与开发指南见 [docs/GUIDE.md](./docs/GUIDE.md)。

## 📚 官方文档

[Go](https://go.dev/doc/) ·
[miekg/dns](https://pkg.go.dev/github.com/miekg/dns) ·
[OpenWrt](https://openwrt.org/docs/start) ·
[OpenWrt SDK](https://openwrt.org/docs/guide-developer/toolchain/using_the_sdk) ·
[UCI](https://openwrt.org/docs/guide-user/base-system/uci) ·
[dnsmasq](https://openwrt.org/docs/guide-user/base-system/dhcp) ·
[procd](https://openwrt.org/docs/guide-developer/procd-init-scripts) ·
[rpcd / ubus](https://openwrt.org/docs/techref/ubus) ·
[nftables](https://wiki.nftables.org/) ·
[LuCI](https://github.com/openwrt/luci/wiki) ·
[DoT RFC 7858](https://datatracker.ietf.org/doc/html/rfc7858) ·
[DoH RFC 8484](https://datatracker.ietf.org/doc/html/rfc8484)

## 许可证

[Apache License 2.0](./LICENSE)
