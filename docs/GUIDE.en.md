# UpDNS Guide

[简体中文](./GUIDE.md) | **English**

[← Back to README](../README.en.md) · [Repository](https://github.com/garinasset/updns)

This guide is for UpDNS users and maintainers. It describes product behavior, configuration, the build process, and how the LuCI page maps to the backend configuration.

## Product Overview

UpDNS is a DNS service for OpenWrt. It accepts queries over UDP/TCP, selects upstream DNS servers according to domain-list rules, and can write resolved addresses into specified nftables sets. When no rule matches, the default upstreams are used; a fallback rule can also be configured.

Supported upstream protocols: UDP, TCP, DNS over TLS (DoT), and DNS over HTTPS (DoH). UpDNS can be used standalone, or as the upstream of a DNS forwarder such as dnsmasq.

Main capabilities:

- **Rules**: match by domain list; query multiple upstreams sequentially or concurrently; address-family filtering, NXDOMAIN responses, and Zero-IP Blocking (returning 0.0.0.0 / ::).
- **Cache**: positive entries are managed by the record TTL; negative entries use the RFC 2308 TTL from the response SOA. Cache hits return a decremented TTL. Capacity limit and active preload are supported.
- **Active preload** (enabled by default): every 100 ms, schedule entries that have consumed at least 80% of their TTL, including expired entries. Earliest expiration wins, with at most 8 background tasks in flight globally; scans do not wait for a batch to finish. Expired queries can share a running refresh as `Prefetch Hit`; stale DNS data is never served. Failed refreshes delete the corresponding old version and preserve historical hit counts. See Cache below.
- **nftables**: add resolved IPv4 and IPv6 addresses to the sets specified by a rule; optionally also add the endpoint addresses of the upstreams referenced by the rule.
- **LuCI**: manage the service, upstreams, domain lists, cache, logging, and rules from a web UI.
- **CLI**: `updns <domain>` shows the query result, matched rule, cache/upstream path, and nftables set status.

## Configuration and Rules

OpenWrt configuration uses the UCI format:

- Main config: `/etc/config/updns`
- Domain lists: usually under `/etc/updns/domainlists/`, one domain per line
- Default port: `153`, listening on both UDP and TCP

The package ships with five upstreams by default: Tencent-UDP, Google-DoH, Cloudflare-DoH, Tencent-DoH, and AdGuard-DoH. When the local resolver file is enabled, the nameservers in that file are also used as runtime UDP upstreams; the default file is `/tmp/resolv.conf.d/resolv.conf.auto`.

"Default upstreams" means only the servers preconfigured in the packaged configuration file; the program has no built-in upstream DNS servers. The configuration file fully determines the upstream list and order. Upstreams can be edited or deleted without being automatically restored, even when all are deleted. A missing configuration file produces an explicit error rather than a fallback configuration. Local resolver imports are controlled independently by their enable setting.

Upstream address formats:

| Protocol | Example | Default port |
|---|---|---:|
| UDP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| TCP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| DoT | `dns.google` or `dns.google:853` | 853 |
| DoH | `https://dns.google/dns-query` | 443 |

A DoH address must be a full HTTPS URL. For an IPv6 address with a port, use brackets, e.g. `[2001:db8::1]:5353`.

### Domain Lists

A list entry consists of a unique name and a file path; rules reference the file through the list name. Matching is case-insensitive; blank lines and lines starting with `#` are ignored.

A plain domain matches the exact domain and its subdomains: `example.com` matches `example.com` and `www.example.com`, but not `superexample.com`. A leading `*` changes this: `*example.com` matches `example.com`, `www.example.com`, and also `superexample.com`.

### Rule Behavior

Rules are evaluated in order and evaluation stops at the first applicable match. A rule may reference multiple domain lists; matching any one of them satisfies the domain condition. LuCI supports drag-and-drop ordering of regular rules; selecting "Do Not Match (Fallback Rule)" in the rule's "If the Domain Matches" drop-down (`domain_match='0'`) makes it a fallback rule. There can be at most one fallback rule and it always runs after the regular rules.

| Action | Behavior |
|---|---|
| `ipv4+ipv6` | Return both IPv4 and IPv6 results |
| `ipv4only` | Return IPv4 results only |
| `ipv6only` | Return IPv6 results only |
| `nxdomain` | Return NXDOMAIN directly, without querying upstreams; the response is cached |
| `zeroip` | Zero-IP Blocking: return `0.0.0.0` for A queries and `::` for AAAA queries, without querying upstreams; the response is cached |

Responses of `nxdomain` and `zeroip` are cached with a fixed TTL of 60 seconds and are not written to nftables sets.

The `ipv4+ipv6`, `ipv4only`, and `ipv6only` actions filter only A/AAAA records; MX, TXT, NS, SOA, SRV, PTR, and other non-address records, as well as CNAMEs, are returned verbatim. HTTPS/SVCB records are not returned because their address hints would bypass address-family filtering and nftables sets.

A rule (except `nxdomain` and `zeroip`) can select one or more upstreams and specify separate nftables sets for IPv4/IPv6. With concurrent query enabled, the rule's selected upstreams are queried concurrently and the first valid DNS response wins; otherwise they are queried one by one in configured order until a valid response is obtained. If no rule matches and there is no fallback rule, the whole upstream DNS list (if any) is used; when the local resolver is enabled, its upstreams come before the other upstreams.

Both modes follow the same response and caching rules:

- **Response validity:** The response must have the response flag set and match the query ID, opcode, question name (case-insensitive), type, and class. Validity does not depend on RCODE: valid `NOERROR`, `NXDOMAIN`, `FORMERR`, `SERVFAIL`, `REFUSED`, `NOTIMP`, and other responses can all win. Error replies that omit Question still report their actual RCODE and question count for diagnostics.
- **CNAME handling:** A `NOERROR` response must contain the requested record type, or a complete CNAME chain and its terminal result. For an incomplete CNAME chain, the terminal name is queried on the same upstream with the original rule, timeout budget, and cancellation signal, without rule re-matching. At most 16 hops are allowed, and loops across responses are detected. A referral with NS delegation but no SOA is not a terminal result; an `NXDOMAIN` must not contain terminal records of the requested type.
- **Cache eligibility:** Cache eligibility is evaluated independently after a response wins and never affects the winner or what the client receives. Positive records are cached by TTL; NODATA and NXDOMAIN are negatively cached only with a valid SOA covering the terminal name. Responses without an SOA or otherwise not cacheable are still returned normally but are not cached. Error-RCODE responses are not cached, and their addresses are not written to nftables sets.
- **Concurrent query:** After the first valid final response wins, the other upstream requests are canceled immediately: in-flight UDP/TCP/DoT connections are closed and DoH HTTP requests are canceled. Requests already sent to remote servers cannot be retracted.
- **Truncation:** A UDP response with `TC=1` triggers a TCP retry to the same upstream address and port, sharing the timeout budget; a failed or still-truncated retry cannot win. Truncated TCP, DoT, and DoH responses are not used directly either.
- **Failure handling:** When no upstream yields a valid final response, ordinary queries return a locally generated `SERVFAIL`, and types such as HTTPS/SVCB keep their existing failure suppression; the specific causes are logged at warn level. A winning valid DNS response never enters failure handling.

### Cache

Keys use lowercase fully qualified domain names and query types; A and AAAA remain independent. HitCount starts at 0 and increments for each valid cache hit, including CLI hits. Initial upstream queries, refreshes, and refresh waiters do not count as hits. Every replacement inherits the current count, including hits received during network I/O. Counts saturate rather than overflow and do not decay.

EvictionPool is a bounded history with the same capacity as the cache. It stores only keys and HitCount, not DNS packets. Ordinary deletion, expired reclamation, and capacity eviction move counts into history. Repeated keys move to the tail; a full pool overwrites its oldest history record. Re-admission consumes the historical count; if history has been overwritten, the new entry starts at 0.

The former 30-second periodic cleanup is removed. When admission needs space, reclaim all expired entries first, then evict the lowest HitCount. Ties favor eviction of the entry least recently hit by a client (insertion order for entries never hit). Background refresh does not improve this tie-break priority.

Expired entries remain available for scheduling and count inheritance, never for answering stale data. With preload disabled, expired entries can remain until capacity reclamation or a client query resolves them again. Cache size reports occupied entries, including expired ones, not only immediately usable answers.

Cache and history are memory-only. Explicit clear, stop, restart, and OpenWrt configuration reload (implemented as restart) reset cache, history, and task state together; clear does not transfer all entries into history. Monotonic entry versions and clear epochs prevent old tasks from deleting or overwriting newer entries or resurrecting evicted/cleared entries.

#### Preload scheduling and shared queries

Every 100 ms, entries with at most 20% of their original lifetime remaining become pending, including expired entries. Pending excludes in-flight tasks. Select the earliest original expiration first and fill free slots up to 8 global background tasks. Slow tasks do not block later scans. Preload does not filter entries by HitCount or recent demand.

- Unexpired: immediately return decremented TTLs as `Cache Hit`.
- Expired with a running preload: join its result as `Prefetch Hit`; return fresh data or failure, never expired data. This path means a refresh result was shared and does not increment cache HitCount. CLI and logs preserve the path.
- Expired without a running preload, or absent: immediately resolve as `Upstream Query`, without consuming a background slot. Other clients and scans share that key's in-flight operation instead of issuing duplicate queries.

Successful cacheable refreshes replace only their original entry version and inherit its count. A failed refresh, TTL 0 result, or uncacheable winning response (such as an error RCODE or an SOA-less negative answer) deletes that old version and moves its count into EvictionPool, even when the old answer still has valid lifetime. This intentionally sacrifices remaining validity; deleted entries are not periodically retried. Later client requests can resolve again and inherit history.

Upstream TTL 0 answers, SOA-less NOERROR/NXDOMAIN responses, and error-RCODE responses are never cached or periodically preloaded. These winning responses are still delivered to waiters with their original RCODE. Negative lifetimes remain `min(SOA TTL, SOA.MINIMUM)`, further bounded by CNAME lifetimes. Upstream selection is unchanged. Eight background tasks can produce more than eight network requests through concurrent upstreams, CNAME follow-ups, or TCP retries; 100 ms is not a completion deadline.

Waiters receive independent copies of DNS results. Network I/O holds no cache or scheduling lock. Clear cancels existing tasks and wakes waiters; shutdown cancels and joins all resolution tasks and the scheduling loop.

#### Data structures

Hash lookup and indexed min-heaps track expiration, LFU eviction, 80% refresh deadlines, and ready-task expiration priority. EvictionPool uses a hash table and doubly linked list for O(1) history operations. Priority updates, insertion/deletion, and per-entry scheduling are O(log N); idle scans are O(1). Entries move to ready only when due, avoiding full-cache scans, sorting, and DNS record cloning every 100 ms. Deletion removes all indices, preventing unbounded stale heap nodes. Cache/history space is O(N), plus the actual shared in-flight query table.

A rule can also add the IP addresses of the endpoints of its referenced upstreams to the corresponding nftables sets. Hostnames (including DoH URL hostnames) are resolved to IPs and refreshed periodically; new addresses are added to the sets, and old addresses are not automatically removed from shared sets.

### Basic UCI Example

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

On a fresh install `enabled=0`, so the service does not start automatically. Defaults: port `153`, local resolver used, sequential queries, cache enabled with capacity `4096`, active preload enabled, query logging off.

The "Block" drop-down in the rule editor (No Blocking / NXDOMAIN Response / Zero-IP Response) is a UI control for the `action` field, not a separate UCI field: NXDOMAIN Response maps to `action='nxdomain'` and Zero-IP Response to `action='zeroip'`. Selecting either hides and clears the upstream, nftables set, and "And Return" options.

A rule that selects "Do Not Match (Fallback Rule)" (`domain_match='0'`) is a fallback rule. Only one is allowed; it runs after no other rule has matched, and the LuCI page automatically keeps it after the regular rules.

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

When the service is running, the CLI queries through the local control interface, using the service's rules and cache and showing nftables set information; when the service is not running, it queries independently without the service cache. By default it performs A and AAAA queries separately.

When a query fails the full report is still printed: `Response` shows `ERROR` and an `ERRORS` section gives one normalized short reason per query type (for example `Upstream timeout`, `Upstream connection refused`, `Upstream host unresolvable`, `Network not ready`, `No upstream configured`, `All upstreams failed`), and the command exits non-zero. If only one of A/AAAA fails, the other result is shown normally and the exit code is 0 as long as one succeeds. The frame width adapts to the terminal (minimum 48, maximum 80 columns).

## Service Management

The service is managed by OpenWrt init/procd. `general.enabled` defaults to `0`; saving and applying configuration triggers an init script reload, which starts or stops the service according to that field.

```sh
/etc/init.d/updns enable
/etc/init.d/updns disable
/etc/init.d/updns start
/etc/init.d/updns stop
/etc/init.d/updns restart
/etc/init.d/updns reload
/etc/init.d/updns status
```

- `enable` enables start at boot.
- `disable` disables start at boot.
- `start` starts the service process; if it is already running, it returns directly.
- `restart` is equivalent to `stop` (stop the process and clear nftables sets) followed by `start`; the cache is rebuilt when the process restarts.
- `reload`
  - with `general.enabled=1`, goes through the restart path;
  - with `general.enabled=0`, goes through the stop path.
- `stop` stops the service process (the cache is released when the process exits) and, after the process has stopped, clears the nftables sets referenced by the current configuration.

Saving and applying configuration in LuCI triggers `reload`.

Boot start is managed by init and is on by default, but `general.enabled` defaults to `0`, so the service process is not started. Once `general.enabled` is set to `1`, the service process is also started at boot if boot start is enabled. The existing configuration is preserved on upgrade and is not overwritten.

To enable or disable the service, use LuCI, or keep UCI `general.enabled` in sync.

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

You can also upload and install with the repository's [`deploy.sh`](../deploy.sh):

```sh
./deploy.sh ./artifacts/updns-<version>.apk \
    ./artifacts/luci-app-updns-<version>.apk \
    ./artifacts/luci-i18n-updns-zh-cn-<version>.apk \
    root@router-host
```

For test/development environments, see [`scripts/build-openwrt.sh`](../scripts/build-openwrt.sh) and the deploy script; for official releases, use an SDK matching the target firmware and verify on an OpenWrt device.

## Troubleshooting

- **Service not running**: check `general.enabled` in `/etc/config/updns`, view `logread -e updns`, and confirm the listening port is not in use.
- **Upstream query failures**: check the upstream address, protocol, network connectivity, and the device CA certificates; set `log_level` to `debug` for more information.
- **Cache not effective**: confirm `cache_enabled=1`; NXDOMAIN and Zero-IP rule responses do not query upstreams but are cached.
- **nftables update failures**: confirm `nftables-json` is installed, the target set exists, and the set family matches the address family.
- **LuCI cannot read data**: verify the rpcd ACL includes the required UCI, file, or command permissions, and reload the rpcd/LuCI services.

UpDNS's regular logs are collected by procd and can be viewed with `logread`. Per-query DNS logging also requires `log_query`.

A winning valid DNS response (including upstream error RCODEs such as `SERVFAIL` or `REFUSED`, and SOA-less negative responses) does not produce a query-failure warning; with `log_query` enabled it is logged as a regular per-query line with its actual RCODE. Only when no upstream yields a valid final response (timeouts, question mismatches, referrals, truncation, failed CNAME follow-ups, and so on) does the existing failure-logging policy emit a warning listing each upstream's specific cause.

With "Log Queries" enabled and `add_subnet` configured in dnsmasq, the log can record `client` and `ecs`.

## Project Structure

| Area | Location | Purpose |
|---|---|---|
| Entry point and CLI | `cmd/updns/` | Service startup, query command, and output |
| Config, rules, upstreams, cache | `config/`, `rules/`, `resolver/`, `cache/` | UCI parsing, matching, DNS queries, and caching |
| DNS server and control interface | `server/` | Request handling, control interface, status, and cache logic |
| nftables and logging | `nftset/`, `logging/` | Set operations and logging |
| OpenWrt packages and LuCI | `openwrt/` | init script, default UCI config, UI, ACL, and translation |
| Build and deploy | `scripts/build-openwrt.sh`, `deploy.sh`, `Makefile` | Local build, SDK packaging, and device install |

## License

[Apache License 2.0](../LICENSE)
