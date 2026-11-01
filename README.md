# UpDNS

**简体中文** | [English](./README.en.md)

UpDNS 是一个基于 [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/) 的 OpenWrt 上游 DNS 提供商：对外提供统一的 DNS 服务入口，对内按规则将查询分发至指定的上游 DNS。

无论是作为**独立的 DNS 服务器**直接提供解析服务（监听端口可自行设置），还是作为 **dnsmasq 等 DNS 应用的上游 DNS** （默认监听 `153` 端口），UpDNS 都能胜任。

## 🧩 技术栈

| 技术 | 作用 |
| --- | --- |
| [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/) | 后端开发语言，编译为单个可执行文件，实现 DNS 服务、规则匹配与缓存 |
| [![miekg/dns](https://img.shields.io/badge/miekg%2Fdns-00ADD8?logo=go&logoColor=white)](https://github.com/miekg/dns) | Go DNS 库，负责 DNS 报文解析、UDP/TCP 服务与上游查询 |
| [![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858) | 加密的上游 DNS 传输协议，经 TLS 查询上游 |
| [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484) | 基于 HTTPS 的上游 DNS 传输协议；在允许 HTTPS 的网络中可能更易连通，但实际可达性取决于网络策略 |
| [![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) | 目标运行平台，提供路由器系统、软件包与服务管理 |
| [![APK](https://img.shields.io/badge/APK-OpenWrt%20Packages-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/additional-software/apk) | OpenWrt 25.12 起使用的软件包格式，用于分发后端、LuCI 与中文翻译 |
| [![OpenWrt SDK](https://img.shields.io/badge/OpenWrt%20SDK-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/toolchain/using_the_sdk) | 交叉编译与打包工具链，在 Linux x86_64 上构建 APK |
| [![UCI](https://img.shields.io/badge/UCI-OpenWrt%20Config-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/uci) | OpenWrt 统一配置接口，保存端口、上游、域名列表与规则 |
| [![dnsmasq](https://img.shields.io/badge/dnsmasq-DNS%20Forwarder-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/dhcp) | 可选的 DNS 转发集成：dnsmasq 可将查询转发到 UpDNS |
| [![procd](https://img.shields.io/badge/procd-init%20%26%20process-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/procd-init-scripts) | OpenWrt 进程管理，负责服务启停、常驻与日志收集 |
| [![nftables](https://img.shields.io/badge/nftables-Netfilter-0F4C81)](https://netfilter.org/projects/nftables/) | 可选的 Linux 防火墙集成；按规则配置将适用的解析地址写入 nft set，供路由或过滤使用 |
| [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) | OpenWrt Web 管理界面框架，提供 UpDNS 配置页面与中文翻译 |
| [![rpcd / ubus](https://img.shields.io/badge/rpcd%20%2F%20ubus-OpenWrt%20RPC-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/techref/ubus) | LuCI 与 OpenWrt 的 RPC 接口，用于读取运行状态及所需文件 |
| [![JavaScript](https://img.shields.io/badge/JavaScript-F7DF1E?logo=javascript&logoColor=black)](https://developer.mozilla.org/docs/Web/JavaScript) | LuCI 视图逻辑，实现规则编辑、动态选择与运行状态展示 |
| [![gettext](https://img.shields.io/badge/gettext-PO-A42E2B?logo=gnu&logoColor=white)](https://www.gnu.org/software/gettext/) | 国际化翻译格式，维护简体中文界面文本 |
| [![GitHub](https://img.shields.io/badge/GitHub-181717?logo=github&logoColor=white)](https://github.com/) | 代码托管与版本管理 |
| [![Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-D22128?logo=apache&logoColor=white)](./LICENSE) | 开源许可证 |

## 🚀 工作流程

**客户端 → UpDNS → 规则匹配 → 缓存 / 上游 DNS（UDP · TCP · DoT · DoH → NFT SETS → 返回结果**

UpDNS 作为统一入口接收查询，按规则匹配域名列表，命中缓存则直接返回，否则查询所选上游。只有在规则配置了 nftables 集合且解析结果适用时，UpDNS 才会将地址写入集合；这不是每次查询的必经步骤。

## 💻 CLI 输出示例

以下输出中的域名、地址、规则、上游及 nftables 集合状态均为示意值，实际结果取决于配置、缓存和网络。

```text
root@OpenWrt:~# updns sysupgrade.openwrt.org

  DOMAIN
  ──────────────────────────────────────────
  Domain       : sysupgrade.openwrt.org.
  Query Type   : A / AAAA
  Query Path   : Cache Hit

  DNS RESULT
  ──────────────────────────────────────────
  Matched Rule : NL Access
  Action       : IPv4 + IPv6
  Response     : NOERROR
  Upstream     : Cloudflare-DoH

  NFT SETS
  ──────────────────────────────────────────
  IPv4         : inet/fw4/nl4
                 ✓ Contains resolved IPv4
  IPv6         : inet/fw4/nl6
                 ✓ Contains resolved IPv6

  ANSWERS
  ──────────────────────────────────────────
  • 45.140.183.87
  • 2001:678:6e1:1001:be24:11ff:fe23:4c6d
```

## 🖥️ LuCI 配置示例

安装 UpDNS 与 LuCI 应用后，在 OpenWrt 管理界面进入 **服务 → UpDNS** 即可配置使用，界面包含运行状态、设置选项卡（设置及端口、上游 DNS、域名列表、缓存、日志）和规则列表。配置项旁的 `?` 提示会说明其作用。

配置文件位置：

- UCI 配置：`/etc/config/updns`
- 域名列表：`/etc/updns/domainlists/`

## ⚙️ 与 dnsmasq 配合时的缓存建议

将 UpDNS 作为 dnsmasq 的上游 DNS 时，建议**只在 UpDNS 中启用缓存**，并将 dnsmasq 的缓存大小设置为 `0`（即关闭 dnsmasq 缓存）：

```bash
uci set dhcp.@dnsmasq[0].cachesize='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

这样可以避免 dnsmasq 与 UpDNS 各自缓存带来的状态分散，以及规则或上游变更后旧结果仍留在 dnsmasq 缓存中的情况。禁用 dnsmasq 缓存后，转发到 UpDNS 的查询由 UpDNS 管理缓存；UpDNS 日志与 CLI 的 `Cache Hit` / `Upstream Query` 只反映实际到达 UpDNS 的查询，未转发给 UpDNS 的 dnsmasq 本地或缓存响应不会出现在其中。

## 🔗 仓库

[github.com/garinasset/updns](https://github.com/garinasset/updns)

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
