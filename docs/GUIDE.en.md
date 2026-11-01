# UpDNS Guide

[简体中文](./GUIDE.md) | **English**

[← Back to README](../README.en.md) · [Repository](https://github.com/garinasset/updns)

This guide is for UpDNS users and maintainers. It describes product behavior, configuration, the build process, and how the LuCI page maps to the backend configuration.

## Product Overview

UpDNS is a DNS service for OpenWrt. It accepts queries over UDP/TCP, selects upstream DNS servers according to domain-list rules, and can write resolved addresses into specified nftables sets. When no rule matches, the default upstreams are used; a fallback rule can also be configured.

Supported upstream protocols: UDP, TCP, DNS over TLS (DoT), and DNS over HTTPS (DoH). UpDNS can be used standalone, or as the upstream of a DNS forwarder such as dnsmasq.

Main capabilities:

- **Rules**: match by domain list; query multiple upstreams sequentially or concurrently; address-family filtering and NXDOMAIN responses.
- **Cache**: positive entries are managed by the record TTL; negative entries use the RFC 2308 TTL from the response SOA. Cache hits return a decremented TTL. Capacity limit and active preload are supported.
- **Active preload** (enabled by default): a background loop scans the cache every 100 ms and re-resolves entries that have passed 75% of their TTL but have not yet expired (including rule matching and nftables writes). On success the cache entry is overwritten with the new result. Preload does not distinguish whether an entry is still being queried, and does not serve expired data when a refresh fails; entries that fail to refresh simply expire naturally.
- **nftables**: add resolved IPv4 and IPv6 addresses to the sets specified by a rule; optionally also add the endpoint addresses of the upstreams referenced by the rule.
- **LuCI**: manage the service, upstreams, domain lists, cache, logging, and rules from a web UI.
- **CLI**: `updns <domain>` shows the query result, matched rule, cache/upstream path, and nftables set status.

## Configuration and Rules

OpenWrt configuration uses the UCI format:

- Main config: `/etc/config/updns`
- Domain lists: usually under `/etc/updns/domainlists/`, one domain per line
- Default port: `153`, listening on both UDP and TCP

The package ships with four DoH upstreams by default: Google, Cloudflare, DoH.pub, and AdGuard. When the local resolver file is enabled, the nameservers in that file are also used as runtime UDP upstreams; the default file is `/tmp/resolv.conf.d/resolv.conf.auto`.

Upstream address formats:

| Protocol | Example | Default port |
|---|---|---:|
| UDP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| TCP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| DoT | `dns.google` or `dns.google:853` | 853 |
| DoH | `https://dns.google/dns-query` | 443 |

A DoH address must be a full HTTPS URL. For an IPv6 address with a port, use brackets, e.g. `[2001:db8::1]:5353`.

### Domain Lists

A list entry consists of a unique name and a file path; rules reference the file through the list name. Matching is case-insensitive; blank lines and lines starting with `#` are ignored. A plain domain matches the exact domain and its subdomains; `*.example.com` matches `example.com` and its subdomains. Relative file paths are resolved against `/etc/updns/domainlists/`.

### Rule Behavior

Rules are evaluated in order and evaluation stops at the first applicable match. A rule may reference multiple domain lists; matching any one of them satisfies the domain condition. LuCI supports drag-and-drop ordering of regular rules; turning off "domain match" creates a fallback rule, of which there can be at most one and which always runs after the regular rules.

| Action | Behavior |
|---|---|
| `ipv4+ipv6` | Return both IPv4 and IPv6 results |
| `ipv4only` | Return IPv4 results only |
| `ipv6only` | Return IPv6 results only |
| `nxdomain` | Return NXDOMAIN directly, without querying upstreams and without caching the response |

A rule can select one or more upstreams and specify separate nftables sets for IPv4/IPv6. With concurrent query enabled, the selected upstreams are queried concurrently and the first valid answer is used; otherwise they are queried one by one in configured order until one succeeds. If no rule matches and there is no fallback rule, the default upstreams are used; when the local resolver is enabled, its upstreams come before the other default upstreams.

A rule can also add the IP addresses of the endpoints of its referenced upstreams to the corresponding nftables sets. Hostnames (including DoH URL hostnames) are resolved to IPs and refreshed periodically; new addresses are added to the sets, and old addresses are not automatically removed from shared sets.

`restart` first tries to clear the nftables sets referenced by the current configuration; the cache is rebuilt when the service process restarts. `reload` goes through the restart path when `general.enabled=1`, and only stops the service (without clearing nftables sets) when `general.enabled=0`. Saving and applying configuration in LuCI triggers `reload`.

### Basic UCI Example

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option concurrent_query '0'
    option cache_enabled '1'
    option cache_size '8192'
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
    list upstreams 'google_doh'
    option action 'ipv4+ipv6'
    list ipv4_nftsets 'inet/fw4/example_v4'
    list ipv6_nftsets 'inet/fw4/example_v6'
    option add_upstream_ips '0'
```

See [`openwrt/updns/root/etc/config/updns`](../openwrt/updns/root/etc/config/updns) for the full default configuration. Log levels are `debug`, `info`, `warn`, or `error`; per-query DNS logging additionally requires `log_query`.

## LuCI Product and Frontend Reference

The entry is **Services → UpDNS**, at route `/admin/services/updns`. The page is divided into Status, Settings & Port, Upstream DNS, Domain Lists, Cache, Logging, and Rules areas.

### Page and Configuration Model

| Page area | UCI section / fields | Product behavior |
|---|---|---|
| Status | Service status, cache status file | Shows service status and cache entry count, refreshed periodically |
| Settings & Port | `updns.general`: `enabled`, `port`, `use_local_resolv`, `local_resolv_file`, `concurrent_query` | Enable/disable service, listen port, local resolver, upstream query mode |
| Upstream DNS | `updns.upstream`: `name`, `address`, `protocol`, `timeout` | Manage UDP/TCP/DoT/DoH upstreams |
| Domain Lists | `updns.domainlist`: `name`, `file` | Upload or manage list files referenced by rules |
| Cache | `updns.general`: `cache_enabled`, `cache_size`, `active_preload` | Control cache, capacity, and active preload |
| Logging | `updns.general`: `log_query`, `log_level` | Control query logging and log level |
| Rules | `updns.rule`: `name`, `domain_match`, `domain_lists`, `upstreams`, `action`, `ipv4_nftsets`, `ipv6_nftsets`, `add_upstream_ips` | Set match conditions, upstreams, response type, and nftables handling |

On a fresh install `enabled=0`, so the service does not start automatically. Defaults: port `153`, local resolver used, sequential queries, cache enabled with capacity `8192`, active preload enabled, query logging off. A new rule matches domain lists by default and returns `ipv4+ipv6`.

"Return NXDOMAIN" is a UI control for `action='nxdomain'`, not a separate UCI field. When selected, no upstream or nftables set is needed. The page also keeps the fallback rule after the regular rules.

### Frontend Files and Maintenance Notes

| Content | Source location |
|---|---|
| Page view, forms, and status display | [`config.js`](../openwrt/luci-app-updns/root/www/luci-static/resources/view/updns/config.js) |
| Menu route | [`menu.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/luci/menu.d/luci-app-updns.json) |
| rpcd/ubus permissions | [`acl.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/rpcd/acl.d/luci-app-updns.json) |
| Simplified Chinese translation | [`updns.po`](../openwrt/luci-app-updns/po/zh_Hans/updns.po) |
| Backend default UCI config | [`updns`](../openwrt/updns/root/etc/config/updns) |
| Package metadata and dependencies | [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile), [`openwrt/updns/Makefile`](../openwrt/updns/Makefile) |

The page reads and saves configuration through UCI; local resolver entries appear as auto-generated `local-resolv-N` upstreams. Service status comes from rpcd `service.list`, the cache entry count from `/tmp/updns_cache_size`, and nftables sets from the page calling `nft -j list sets`. Domain lists are read and written through the rpcd file interface.

When changing the page, check the related UCI fields, the rpcd ACL, and backend behavior together. The ACL grants explicit file paths; when adding a new status file, file read/write path, or system command, update the ACL as well. Visible text uses `_()` and the PO translation must be kept in sync. If you change form sections or tab layout, review the mapping and ordering logic in the page.

## dnsmasq Integration

To use UpDNS as the dnsmasq upstream, add the local forwarding address:

```sh
uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#153'
uci set dhcp.@dnsmasq[0].cachesize='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

Enabling the cache only in UpDNS is recommended, to avoid the extra state-debugging effort and update delay of two cache layers. The two layers expire independently according to their own TTL and cache policy; TTLs are not necessarily extended twice. If you want dnsmasq to use only UpDNS, set `noresolv='1'`. UpDNS can also serve DNS on its own; before switching to port `53`, make sure that port is not occupied by dnsmasq or another program.

## CLI and Service Management

```sh
updns example.com
```

When the service is running, the CLI queries through the local control interface, using the service's rules and cache and showing nftables set information; when the service is not running, it queries independently without the service cache. By default it performs A and AAAA queries separately.

The service is managed by OpenWrt init/procd. `general.enabled` defaults to `0`; saving and applying configuration triggers an init script reload, which starts or stops the service according to that field.

```sh
# start only runs when general.enabled=1
/etc/init.d/updns start
/etc/init.d/updns stop
/etc/init.d/updns restart
/etc/init.d/updns reload
/etc/init.d/updns status
```

To enable or disable the service, use LuCI, or keep UCI `general.enabled` and the init boot-enable state in sync. `restart` tries to clear the nftables sets referenced by rules; `reload` performs that cleanup only when the service is enabled.

## Build, Install, and Test

### Local Development

Requires Go 1.21 or later. Run on a macOS/Linux development machine; locally built binaries are for development and checks and must not be installed directly on the router.

```sh
make build
make test
make lint
```

The binary is output to `bin/updns`. `make test` uses the race detector; `make lint` runs gofmt and go vet.

### OpenWrt APK

Use an SDK matching the target OpenWrt version; the build script in this repository produces APKs with an x86_64 SDK. Put the source and SDK on a Linux x86_64 build host and run from the project root:

```sh
./scripts/build-openwrt.sh /path/to/openwrt-sdk
```

The script syncs feeds, builds `updns` and `luci-app-updns`, and produces the Simplified Chinese translation package per the LuCI build rules. To install a rebuild as a same-version update, bump the package version first:

- Backend: `PKG_RELEASE` in [`openwrt/updns/Makefile`](../openwrt/updns/Makefile)
- LuCI and translation: `PKG_VERSION` or `PKG_RELEASE` in [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile)

Generated APKs are under `bin/packages/x86_64/` in the SDK, in the `packages/` and `luci/` subdirectories. Exact filenames depend on the SDK output.

The APKs are not signed with a trusted key. Copy the three packages to the device and install:

```sh
apk add --allow-untrusted \
    /tmp/updns-<version>.apk \
    /tmp/luci-app-updns-<version>.apk \
    /tmp/luci-i18n-updns-zh-cn-<version>.apk
```

You can also use the repository's [`deploy.sh`](../deploy.sh) to upload and install. For test/development environments see [`scripts/build-openwrt.sh`](../scripts/build-openwrt.sh) and the deploy script; official releases should use an SDK matching the target firmware and be verified on an OpenWrt device.

## Troubleshooting

- **Service not running**: check `general.enabled` in `/etc/config/updns`, view `logread -e updns`, and confirm the listen port is not in use.
- **Upstream query failures**: check the upstream address, protocol, network connectivity, and the device CA certificates; set `log_level` to `debug` for more detail.
- **Cache not effective**: confirm `cache_enabled=1`; NXDOMAIN rule responses do not query upstreams and are not cached.
- **nftables update failures**: confirm `nftables-json` is installed, the target set exists, and the set family matches the address family.
- **LuCI cannot read data**: verify the rpcd ACL includes the required UCI, file, or command permissions, and reload the rpcd/LuCI services.

UpDNS regular logs are collected by procd and can be viewed with `logread`. Per-query DNS logging also requires `log_query`.

With "query logging" enabled and `add_subnet` configured in dnsmasq, the log can record `client` and `ecs`.

## Project Structure

| Area | Location | Purpose |
|---|---|---|
| Entry point and CLI | `cmd/updns/` | Service startup, query command, and output |
| Config, rules, upstreams, cache | `config/`, `rules/`, `resolver/`, `cache/` | UCI parsing, matching, DNS queries, and caching |
| DNS server and control interface | `server/` | Request handling, control interface, status, and cache logic |
| nftables and logging | `nftset/`, `logging/` | Set operations and logging |
| OpenWrt package and LuCI | `openwrt/` | init script, UCI defaults, UI, ACL, and translation |
| Build and deploy | `scripts/build-openwrt.sh`, `deploy.sh`, `Makefile` | Local build, SDK packaging, and device install |

## License

[Apache License 2.0](../LICENSE)
