# Turnout DNS

[简体中文](./README.md) | **English**

Turnout DNS (application name: `updns`) is a local DNS server for OpenWrt. Compared with dnsmasq, it offers more flexible DNS routing and nftables set integration, along with DoT/DoH upstream support, ad blocking, and DNS auditing.

On your OpenWrt network, updns can run as a standalone local DNS server or as the upstream DNS server of dnsmasq.

## 🧩 Tech Stack

| Technology | Purpose |
| --- | --- |
| [![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](https://go.dev/) | Backend language; compiles to a single executable implementing the DNS service, rule matching, and caching |
| [![miekg/dns](https://img.shields.io/badge/miekg%2Fdns-00ADD8?logo=go&logoColor=white)](https://github.com/miekg/dns) | Go DNS library for message parsing, UDP/TCP serving, and upstream queries |
| [![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858) | Encrypted upstream DNS transport over TLS |
| [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484) | Upstream DNS transport over HTTPS |
| [![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) | Target platform, providing the router OS, packages, and service management |
| [![APK](https://img.shields.io/badge/APK-OpenWrt%20Packages-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/additional-software/apk) | Package format used by OpenWrt 25.12 and later; distributes the backend, LuCI app, and Chinese translation |
| [![OpenWrt SDK](https://img.shields.io/badge/OpenWrt%20SDK-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/toolchain/using_the_sdk) | Cross-compilation and packaging toolchain for building APKs on Linux x86_64 |
| [![UCI](https://img.shields.io/badge/UCI-OpenWrt%20Config-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/uci) | OpenWrt configuration interface for the port, upstreams, domain lists, and rules |
| [![dnsmasq](https://img.shields.io/badge/dnsmasq-DNS%20Forwarder-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-user/base-system/dhcp) | Optional forwarding integration: dnsmasq can forward queries to updns |
| [![procd](https://img.shields.io/badge/procd-init%20%26%20process-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/guide-developer/procd-init-scripts) | OpenWrt process manager for service start/stop, supervision, and log collection |
| [![nftables](https://img.shields.io/badge/nftables-Netfilter-0F4C81)](https://netfilter.org/projects/nftables/) | Optional Linux firewall integration; rules add applicable resolved addresses to nft sets for routing or filtering |
| [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) | OpenWrt web UI framework for updns configuration and Chinese localization |
| [![rpcd / ubus](https://img.shields.io/badge/rpcd%20%2F%20ubus-OpenWrt%20RPC-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/docs/techref/ubus) | LuCI/OpenWrt RPC interfaces for reading runtime status and required files |
| [![JavaScript](https://img.shields.io/badge/JavaScript-F7DF1E?logo=javascript&logoColor=black)](https://developer.mozilla.org/docs/Web/JavaScript) | LuCI view logic for rule editing, dynamic selectors, and runtime status |
| [![gettext](https://img.shields.io/badge/gettext-PO-A42E2B?logo=gnu&logoColor=white)](https://www.gnu.org/software/gettext/) | Internationalization format for Simplified Chinese UI text |
| [![GitHub](https://img.shields.io/badge/GitHub-181717?logo=github&logoColor=white)](https://github.com/) | Source hosting and version control |
| [![GitHub Actions](https://img.shields.io/badge/GitHub%20Actions-2088FF?logo=githubactions&logoColor=white)](https://docs.github.com/en/actions) | CI/CD automation for automatically deploying code changes to the production server |
| [![Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-D22128?logo=apache&logoColor=white)](./LICENSE) | Open-source license |

## 🚀 Workflow

**Client → updns → Rule matching → Local data / Cache / Upstream DNS (UDP · TCP · DoT · DoH) → nftset → Response**

updns matches domain rules and processes blocking rules first, followed by hosts, local DHCP data, and valid cache entries. It then determines whether to forward the query to upstream DNS servers. If no rule matches and no fallback rule is configured, the entire upstream DNS list serves as the default rule's upstream group.

## 💻 DNS Auditing — CLI Output Example

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

## 🖥️ LuCI Support

After installing updns and its LuCI app, open **Services → Turnout DNS** in the OpenWrt web interface.

## 📦 Installation

### Version Support

The official package repository currently provides prebuilt **APK** packages only for **OpenWrt 25.12**, with architecture-specific builds. Prebuilt packages are not currently available for historical major OpenWrt releases. On other versions, you must manually build the packages using the corresponding OpenWrt SDK or source tree.

### Installation Steps

The following instructions apply to OpenWrt 25.12:

```sh

# Download the repository signing public key
wget -O /tmp/updns-public.pem https://downloads.garinasset.com/updns/openwrt/signing.pem

# Verify the public key SHA-256 checksum
sha256sum /tmp/updns-public.pem

# Install the public key
cp /tmp/updns-public.pem /etc/apk/keys/updns.pem

# Configure the package repository
ARCH="$(apk --print-arch)"
echo "https://downloads.garinasset.com/updns/openwrt/25.12/${ARCH}/packages.adb" >> /etc/apk/repositories.d/customfeeds.list

# Update package indexes and install packages
apk update
apk add updns luci-app-updns luci-i18n-updns-zh-cn
```

Do not use --allow-untrusted when installing packages from the repository to ensure package signature verification and trusted package sources.After upgrading the firmware or changing the device architecture, recheck the repository URL and package compatibility.

## 📖 Detailed Documentation

See [docs/GUIDE.en.md](./docs/GUIDE.en.md) for complete functionality, build and installation instructions, configuration, CLI usage, and development guidance.

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
