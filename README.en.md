# UpDNS

[简体中文](./README.md) | **English**

UpDNS is an upstream DNS provider for OpenWrt, built with [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/). It offers a unified DNS entry point and dispatches queries to the selected upstream DNS servers according to your rules.

It works equally well as a **standalone DNS server** that answers clients directly (the listening port is configurable), or as the **upstream DNS of dnsmasq and other DNS applications**, which forward their queries to it (default port `153`).

## 🧩 Tech Stack

| Technology | Purpose |
| --- | --- |
| [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/) | Backend language; compiles to a single executable implementing the DNS service, rule matching and caching |
| [![miekg/dns](https://img.shields.io/badge/miekg%2Fdns-00ADD8?logo=go&logoColor=white)](https://github.com/miekg/dns) | Go DNS library for message parsing, the UDP/TCP server and upstream queries |
| [![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858) | Encrypted upstream transport: queries upstream servers over TLS |
| [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484) | Upstream DNS transport over HTTPS; it may be more reachable on networks that allow HTTPS, but actual reachability depends on network policy |
| [![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) | Target platform: router OS, packages and service management |
| [![APK](https://img.shields.io/badge/APK-OpenWrt%20Packages-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/additional-software/apk) | Package format used by OpenWrt 25.12 and later; distributes the backend, LuCI app and Chinese translation |
| [![OpenWrt SDK](https://img.shields.io/badge/OpenWrt%20SDK-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/toolchain/using_the_sdk) | Cross-compilation and packaging toolchain; builds the APKs on Linux x86_64 |
| [![UCI](https://img.shields.io/badge/UCI-OpenWrt%20Config-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/uci) | OpenWrt unified configuration interface; stores the port, upstreams, domain lists and rules |
| [![dnsmasq](https://img.shields.io/badge/dnsmasq-DNS%20Forwarder-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/dhcp) | Optional DNS forwarding integration: dnsmasq can forward queries to UpDNS |
| [![procd](https://img.shields.io/badge/procd-init%20%26%20process-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/procd-init-scripts) | OpenWrt process manager; handles service start/stop, respawn and log collection |
| [![nftables](https://img.shields.io/badge/nftables-Netfilter-0F4C81)](https://netfilter.org/projects/nftables/) | Optional Linux firewall integration; applicable resolved addresses are added to nft sets when configured by a rule |
| [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) | OpenWrt web UI framework; provides the UpDNS configuration pages |
| [![rpcd / ubus](https://img.shields.io/badge/rpcd%20%2F%20ubus-OpenWrt%20RPC-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/techref/ubus) | OpenWrt RPC interfaces used by LuCI to read runtime status and required files |
| [![JavaScript](https://img.shields.io/badge/JavaScript-F7DF1E?logo=javascript&logoColor=black)](https://developer.mozilla.org/docs/Web/JavaScript) | LuCI view logic: rule editing, dynamic selectors and runtime status |
| [![gettext](https://img.shields.io/badge/gettext-PO-A42E2B?logo=gnu&logoColor=white)](https://www.gnu.org/software/gettext/) | Internationalization format; maintains the Simplified Chinese UI text |
| [![GitHub](https://img.shields.io/badge/GitHub-181717?logo=github&logoColor=white)](https://github.com/) | Source hosting and version control |
| [![Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-D22128?logo=apache&logoColor=white)](./LICENSE) | Open-source license |

## 🚀 Workflow

**Client → UpDNS → Rule matching → Cache / Upstream DNS (UDP · TCP · DoT · DoH) → NFT Sets → Response**

UpDNS receives queries at a single entry point and matches the domain against your rules. A cache hit is answered directly; otherwise the selected upstreams are queried. UpDNS writes addresses to nftables sets only when a rule configures a set and the DNS result is applicable; this is not a step in every query.

## 💻 CLI Output Example

The domain, addresses, rule, upstream and nftables set state below are illustrative; actual output depends on configuration, cache state and network conditions.

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

## 🖥️ LuCI Configuration

After installing UpDNS and the LuCI app, open **Services → UpDNS** in the OpenWrt web interface. The page contains the running status, a tabbed settings area (Settings and Port, Upstream DNS, Domain Lists, Cache, Logging) and the rule list. The `?` hint next to each option explains what it does.

Configuration locations:

- UCI configuration: `/etc/config/updns`
- Domain lists: `/etc/updns/domainlists/`

## ⚙️ Cache Recommendation When Used with dnsmasq

When UpDNS is the upstream DNS of dnsmasq, we recommend **enabling the cache only in UpDNS** and setting the dnsmasq cache size to `0` (disabling the dnsmasq cache):

```bash
uci set dhcp.@dnsmasq[0].cachesize='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

This avoids split cache state between dnsmasq and UpDNS, as well as old results remaining in dnsmasq after rules or upstreams change. With the dnsmasq cache disabled, UpDNS manages caching for queries forwarded to it. UpDNS log and CLI markers (`Cache Hit` / `Upstream Query`) describe only queries that reach UpDNS; dnsmasq-local answers and cached responses that are not forwarded do not appear there.

## 🔗 Repository

[github.com/garinasset/updns](https://github.com/garinasset/updns)

## 📖 Detailed Documentation

For features, build and installation, configuration, the CLI and development, see [docs/GUIDE.en.md](./docs/GUIDE.en.md).

## 📚 Official Documentation

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

## License

[Apache License 2.0](./LICENSE)
