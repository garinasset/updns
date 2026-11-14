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
| [![GitHub Actions](https://img.shields.io/badge/GitHub%20Actions-2088FF?logo=githubactions&logoColor=white)](https://docs.github.com/en/actions) | CI/CD 自动化，将代码变更自动部署到生产服务器 |
| [![Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-D22128?logo=apache&logoColor=white)](./LICENSE) | 开源许可证 |

## 🚀 工作流程

**客户端 → updns → 规则匹配 → 本地数据 / 缓存 / 上游 DNS（UDP · TCP · DoT · DoH）→ nftset → 返回结果**

updns 根据域名匹配规则，优先处理拦截，再使用 hosts、DHCP 本地数据及有效缓存；最后根据需要决定转发查询至上游 DNS ; 没有匹配规则且没有配置兜底规则时，以整个上游 DNS 列表作为缺省规则内上游;


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


## 📦 安装

### 版本支持说明

目前官方软件源仅提供 **OpenWrt 25.12** 对应架构的预编译 **APK** 软件包。历史 OpenWrt 大版本暂不提供预编译软件包。如需在其他版本上使用，请根据对应版本的 OpenWrt SDK 或源码环境手动编译构建。


### 安装步骤

以下操作适用于 OpenWrt 25.12：

```sh

# 下载软件源签名公钥
wget -O /tmp/updns-public.pem https://downloads.garinasset.com/updns/openwrt/signing.pem

# 核对公钥 SHA-256 预期: 27ffe5d333a95b10c81de1279078036a00a4d169741a1f26c5e4e98e539aeb4e
sha256sum /tmp/updns-public.pem

# 安装公钥
cp /tmp/updns-public.pem /etc/apk/keys/updns.pem

# 配置软件源
ARCH="$(apk --print-arch)"
echo "https://downloads.garinasset.com/updns/openwrt/25.12/${ARCH}/packages.adb" >> /etc/apk/repositories.d/customfeeds.list

# 更新软件源并安装软件包
apk update
apk add updns luci-app-updns luci-i18n-updns-zh-cn
```

不要使用 `--allow-untrusted` 安装软件源中的软件包，以确保软件包来源可信。升级固件或更换设备架构后，应重新检查软件源地址及软件包兼容性。


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
