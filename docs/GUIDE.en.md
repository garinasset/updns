# Turnout DNS Guide

[简体中文](./GUIDE.md) | **English**

[← Back to README](../README.en.md) · [Project repository](https://github.com/garinasset/updns)

This guide is for users and maintainers of Turnout DNS (application name: `updns`). It covers product capabilities, configuration, technical details, builds, and the relationship between LuCI controls and backend configuration.

## Overview

updns is a local DNS server for OpenWrt. Compared with dnsmasq, it offers more flexible DNS routing and nftables set integration, along with DoT/DoH upstream support, ad blocking, and DNS auditing.

On your OpenWrt network, updns can run as a standalone local DNS server or as the upstream DNS server of dnsmasq.

### Main Capabilities

- **Forwarding:** Dynamic Scheduling, Strict Order, and Concurrent Query.
- **Protocols:** UDP, TCP, [![DoT](https://img.shields.io/badge/DoT-DNS%20over%20TLS-4285F4)](https://datatracker.ietf.org/doc/html/rfc7858), and [![DoH](https://img.shields.io/badge/DoH-DNS%20over%20HTTPS-4285F4)](https://datatracker.ietf.org/doc/html/rfc8484).
- **Cache:** Capacity limits and active prefetch, refreshing records before their TTL expires.
- **Logging:** Query logs and system logs.
- **Rules:** Domain matching, ad blocking (Zero-IP or NXDOMAIN), DNS routing, nftables set integration, and address-family filtering.
- **Ecosystem:** Deep [![OpenWrt](https://img.shields.io/badge/OpenWrt-00B5E2?logo=openwrt&logoColor=white)](https://openwrt.org/) integration: UCI configuration, RPC/ubus interfaces, local `resolv.conf.auto` import, nftables, and the [![LuCI](https://img.shields.io/badge/LuCI-Web%20UI-00B5E2?logo=openwrt&logoColor=white)](https://github.com/openwrt/luci) web interface.
- **DNS auditing:** `updns <domain>` traces resolution, including the matched rule, cache/upstream path, nftables sets, and answers.

## Configuration and Technical Details

### OpenWrt Uses UCI Configuration

- Main configuration: `/etc/config/updns`
- Domain lists: `/etc/updns/domainlists/`, one domain per line
- Default port: `153`, listening on both UDP and TCP

The packaged configuration includes five upstreams: Tencent-UDP, Google-DoH, Cloudflare-DoH, Tencent-DoH, and AdGuard-DoH.

- “Default upstreams” means only the entries supplied in the packaged configuration for quick setup. They can be edited or deleted; deleting all of them does not automatically restore them.

### Settings and Port

The LuCI **Settings and Port** tab configures the listening port and related settings.

#### 1. Enable

The **Enable** switch starts or stops updns and corresponds to `general.enabled`.

- OpenWrt init/procd manages the service. `general.enabled` defaults to `0`. Saving and applying configuration triggers the init script to reload, starting or stopping the process according to this field.
- On a fresh install, `general.enabled=0`: the process does not start, and boot autostart is disabled. Enabling the service in LuCI and saving/applying enables boot autostart and starts the service; disabling and saving/applying stops it and disables boot autostart. Upgrades preserve the existing configuration and apply its enabled state.
- The following init commands provide manual service control:

    ```sh
    /etc/init.d/updns enable
    /etc/init.d/updns disable
    /etc/init.d/updns start
    /etc/init.d/updns stop
    /etc/init.d/updns restart
    /etc/init.d/updns reload
    /etc/init.d/updns status
    ```

    - `enable` only enables boot autostart. It neither starts the process immediately nor changes `general.enabled`.
    - `disable` only disables boot autostart. It neither stops the process immediately nor changes `general.enabled`.
    - `start` starts the process only when `general.enabled=1`; it does not start another instance if one is already running.
    - `restart` performs `stop` (stopping the process and clearing nftables sets), then `start`. The restarted process builds a new cache.
    - `reload` enables boot autostart and restarts when `general.enabled=1`; when it is `0`, it stops the service and disables boot autostart.
    - `stop` stops the process without changing `general.enabled` or boot autostart. The in-process cache disappears on exit, and the OS reclaims process memory. The sets referenced by the current configuration are then cleared.

- Saving and applying in LuCI triggers `reload`.

#### 2. Port

The **Port** field configures the UDP and TCP port accepting DNS queries. The default is `153`.

#### 3. Use Local Resolv File

The **Use Local Resolv File** switch corresponds to `use_local_resolv` and is enabled by default.

- When enabled, nameservers from the selected file (default `/tmp/resolv.conf.d/resolv.conf.auto`) are imported as UDP upstreams at the top of the list, in file order. Duplicate addresses and identical addresses already configured as custom upstreams are skipped. The running service periodically checks the file and updates imported upstreams. A missing file imports nothing; read errors are explicitly reported.

#### 4. Upstream DNS Selection Strategy

The **Upstream DNS Selection Strategy** selector corresponds to `query_strategy`. It controls how the current candidate group is queried: either the matching rule's upstreams or the complete upstream list when no rule matches.

- Only `0`, `1`, and `2` are accepted. The default is `2` (Concurrent Query).

    0. **Dynamic Scheduling (0):** A new ordered upstream group initially queries all members concurrently without preference. The first valid final response wins, recording that winner's own request duration `t`. Later queries normally use only that upstream. At `min(2t, its configured timeout)`, the original request remains in flight and the others are added concurrently; the first valid completion wins. Transport failure or an invalid response from the preferred upstream triggers the others immediately. Only a racing win updates the preference and `t`; an ordinary single-upstream success does not. Added requests measure their duration from their own start, excluding the earlier wait; the original upstream retains its head start. There is no fixed query count, periodic reevaluation, or independent background probing. While the preferred upstream remains healthy, faster alternatives are not proactively discovered; minimum latency is not guaranteed.
    1. **Strict Order (1):** Try upstreams in reference order until a valid final response is obtained.
    2. **Concurrent Query (2):** Query the selected upstreams concurrently. The first valid final response wins and cancels the other in-flight requests.

- All three strategies apply to UDP/TCP/DoT/DoH. Dynamic Scheduling adds racing requests based on response latency. Upstream changes and clearing the cache reset preferences and latency baselines.
- Valid cache entries return directly without upstream queries. Expired refreshes and active prefetch first use the entry's original winning upstream, without `2t` hedging. Only if that upstream is unavailable, reaches its configured timeout, or returns an invalid response are other members of the current group tried using the selected strategy. Success updates the entry's path. Rule or service-binding changes revalidate routing rather than retaining an obsolete path. Without a matching rule or fallback rule, the candidate group is the entire current upstream list, including enabled, successfully imported local resolvers. Local resolvers come first only in list order; queries still follow the selected strategy, with no guarantee that local resolvers win first. Valid HTTPS/SVCB target bindings route through their bound rules.
- All strategies share response validation and caching rules:

    - **Response validity:** A response must set the response flag and match the request ID, opcode, question name (case-insensitively), type, and class. RCODE does not determine validity: valid `NOERROR`, `NXDOMAIN`, `FORMERR`, `SERVFAIL`, `REFUSED`, `NOTIMP`, and other responses can win. Error-code responses must also contain the matching original Question. An omitted Question cannot win; failure diagnostics retain the actual RCODE and question count.
    - **CNAME handling:** A `NOERROR` response must include the queried record type or a complete CNAME chain and its terminal result. Incomplete chains are completed on the same upstream, retaining the rule, timeout budget, and cancellation signal, without rematching rules. The maximum is 16 hops, with cross-response loop detection. NS-only referrals without SOA are not terminal results. `NXDOMAIN` must not contain a terminal record of the queried type.
    - **Cache eligibility:** Assessed after a response wins, independently of winning and client delivery. Positive records use their TTL. NODATA/NXDOMAIN require a valid SOA covering the terminal name for negative caching. Responses without SOA or otherwise ineligible for caching still return normally, but are not stored. Error-code responses are not cached, and their addresses are not added to nftables sets.
    - **Concurrent cancellation:** Once a valid final response wins, other requests are canceled: UDP/TCP/DoT close in-flight connections; DoH cancels the HTTP request. Requests already sent to a remote server cannot be recalled.
    - **Truncation:** UDP `TC=1` triggers TCP retry at the same upstream address and port, sharing the timeout budget. Failed or still-truncated retries cannot win. Truncated TCP, DoT, and DoH responses are not directly accepted either.
    - **Failure handling:** If no upstream produces a valid final response, ordinary queries return locally generated `SERVFAIL`. HTTPS/SVCB have a five-second overall budget; failures return empty answers and suppress repeated failed queries for five minutes. PTR also uses failure suppression. Ordinary query failures use rate-limited warn logs; HTTPS/SVCB, PTR, and network-not-ready failures use debug logs. Valid winning DNS responses do not enter failure handling.

### Upstream DNS

The LuCI **Upstream DNS** tab configures available servers using UCI `upstream` sections.

- The packaged configuration includes Tencent-UDP, Google-DoH, Cloudflare-DoH, Tencent-DoH, and AdGuard-DoH.
    - These are editable quick-setup entries, not implicit servers. Deleting all entries does not restore them.
- Default ports are `53` for UDP/TCP, `853` for DoT, and `443` for HTTPS/DoH. Use `host:port` for custom DNS ports (IPv6 example: `[2001:db8::1]:5353`). DoH requires a full URL, such as `https://dns.example:8443/dns-query`.

### Domain Lists

The LuCI **Domain Lists** tab corresponds to UCI `domainlist` sections, using `name` and `file`. Each entry has a unique name and file path; rules reference the list by name.

- Drag using the row's sorting control, then save or save/apply to persist the order. Domain-list order does not change rule references or matching priority; rule order determines priority.
- Files contain one domain per line. Matching is case-insensitive; blank lines and lines beginning with `#` are ignored.
- Normal entries match the exact domain and its subdomains:
    - `example.com` matches `example.com` and `www.example.com`, but not `superexample.com`.
    - A leading `*` changes this: `*example.com` also matches `superexample.com`.
- Each loaded list builds a compressed reverse Trie index for domain-suffix lookup, rather than scanning all entries. Rules are still evaluated in configuration order, without an additional match-result cache.
- One query reuses its own domain's rule match. Blocking returns directly; local, cache, and upstream paths do not rescan rules. HTTPS/SVCB dynamic bindings are rechecked where needed, including at asynchronous resolution start and before returning. New queries and background prefetch each perform their own matching; routes are not cached across requests.

### Cache

The LuCI **Cache** tab controls DNS caching.

#### 1. Enable Cache

The **Enable Cache** switch corresponds to `cache_enabled` and defaults to enabled. Disabling caching also prevents active prefetch from running.

- Positive records use their response TTL. Negative entries use RFC 2308 TTL from SOA. Cache hits return decreasing TTLs. NODATA/NXDOMAIN require a valid SOA covering the terminal name; otherwise they return normally without caching. Error-code responses are not cached.
- On valid NXDOMAIN/NODATA, the SOA record's TTL is normalized to `min(SOA TTL, SOA.MINIMUM)`. Both the initial downstream response and stored SOA use this value. Hits continue decreasing it according to elapsed storage time, without resetting it. A shorter CNAME TTL can expire the whole entry earlier but does not change SOA.MINIMUM. Positive answers and direct SOA queries do not undergo this negative-TTL normalization.
- Validity does not imply cache eligibility: original TTL 0, NOERROR/NXDOMAIN without SOA, and error-code responses are not stored. Winning responses are still delivered to waiters with their original RCODE. Normal negative caching uses `min(SOA TTL, SOA.MINIMUM)`; a CNAME chain cached with a negative terminal result further limits the lifetime.
- **HitCount**
    - The key is the lowercase fully qualified domain plus query type; A and AAAA are separate. HitCount starts at 0 and increments on each valid cache hit, including CLI queries. Initial upstream resolution, prefetch, and waiting for prefetch do not count as hits. Replacement inherits all current hits, including hits during refresh. Counts saturate rather than wrapping, with no time decay.
    - No history is retained after deletion or eviction. Only an existing entry passes its HitCount to its refresh replacement; readding a deleted or evicted key starts at 0.
- **Insertion, deletion, and lifecycle**
    - There is no periodic expired-entry cleanup. An insertion into a full cache evicts one entry with the lowest HitCount, without prioritizing expired entries. Ties evict the entry least recently hit by a client; never-hit entries use initial insertion order. Background refresh does not improve this tie-break priority. A frequently hit expired entry can therefore remain while a valid cold entry is evicted.
    - Expiration does not immediately delete entries, nor permit stale answers. Entries remain for prefetch scheduling and hit-count inheritance. With active prefetch disabled, an expired entry cannot directly answer clients; a client query starts fresh resolution. The original cached upstream is preferred if routing is unchanged and it still belongs to the current group; failure falls back to other upstreams using the current strategy. Such expired entries may remain until capacity eviction or the next client-triggered resolution.
    - The displayed cache count includes expired entries; it is not the number of immediately usable answers.
    - The cache exists only in memory, without persistence across processes. Explicit clearing removes entries and invalidates in-flight results. Stopping waits for service tasks and clears the cache. Restarting, including enabled OpenWrt configuration reloads, destroys the old process's cache and starts empty. Entry versions and clear-generation markers only protect concurrency within one process: old tasks cannot delete or overwrite newer entries, or recreate evicted/cleared entries. They are not persistence or recovery versions. Clearing releases cache references, but unfinished tasks may temporarily retain objects. Go garbage collection can reclaim objects after all references disappear; it does not guarantee immediate return of heap memory to the OS. Clearing a running process's cache therefore need not immediately reduce its memory footprint; process exit lets the OS reclaim memory.

#### 2. Cache Size

The **Cache Size** field corresponds to `cache_size` and limits the number of entries. The default is `2048`.

#### 3. Cache Prefetch

The **Cache Prefetch** switch enables active prefetch, corresponds to `cache_prefetch`, and defaults to enabled.

- Only `cache_prefetch` is used. The former `active_preload` field has no compatibility mapping or automatic migration; if the new field is absent, its default is `1`.
- Every 100 ms, entries that have consumed at least 80% of their original TTL (at most 20% remaining) become pending, including expired entries. Pending excludes in-flight tasks. Tasks start in earliest-original-expiration order. The global background concurrency limit is `max(16, min(ceil(cache_size / 128), 64))`, using configured capacity; the default `2048` gives `16`. Each scan fills free slots; slow tasks do not block later scans. Every entry can be prefetched, without filtering by HitCount or current demand.
- Refresh first queries the entry's original winning upstream. A successful result is used directly; on failure, other members are tried according to the current strategy.
- **Foreground behavior with active prefetch enabled**
    - Unexpired: immediately return decreasing-TTL cache data with path `Cache Hit`.
    - Expired and being prefetched: wait for that same task, with path `Prefetch Hit`. Success returns the new answer; failure returns failure, never expired data. Logs and CLI retain this path. Reusing a prefetch result does not count as a cache hit.
    - Expired but not being prefetched, or never cached: start foreground resolution immediately, with path `Upstream Query`, unconstrained by the background limit. Other foreground requests for the same key and background scans share that task without duplicate queries.
- A successful cacheable refresh with TTL greater than 0 replaces the corresponding old version and inherits HitCount. On failure, TTL 0, or an uncacheable winning response (such as an error code or a negative answer without SOA), an unexpired old entry keeps its answer, original expiration, path, and count, with retry delayed until its original expiration to avoid retries every 100 ms. An expired old entry is deleted, without stale answers or retained hit history.
- Validity does not imply cache eligibility: TTL 0, NOERROR/NXDOMAIN without SOA, and error-code responses are not stored or periodically prefetched. Winners still reach waiters with the original RCODE. Negative TTL uses `min(SOA TTL, SOA.MINIMUM)`, further limited by a CNAME chain when present.
- Background task count is not network request count: foreground queries, upstream concurrency, CNAME completion, and TCP retries can add requests. The 100 ms interval is not a completion deadline.
- Each waiter receives an independent copy of in-flight results, avoiding concurrent modification of DNS messages. Network I/O holds neither cache nor scheduling locks. Clearing cancels existing tasks and wakes waiters; stopping cancels and waits for the scheduler and all resolution tasks.

### Logging

The LuCI **Logging** tab corresponds to `log_query` and `log_level`.

#### 1. Log Queries

The **Log Queries** switch corresponds to `log_query` and is disabled by default.

- Logs and CLI reflect only queries that actually reach updns; other queries cannot appear there.
- For dnsmasq forwarding, only requests reaching updns are recorded.

#### 2. Log Level

The **Log Level** selector corresponds to `log_level`: `error`, `warn`, `info`, or `debug`, defaulting to `info`.

### Rules

The LuCI **Rules** area configures domain matching, ad blocking, DNS routing, nftables set integration, and address-family filtering through UCI `rule` sections.

- Rules are evaluated in order; the first applicable rule wins. A rule can reference multiple domain lists, matching if any one list matches. Ordinary rules support drag sorting.
- Choosing **Do not match (fallback rule)** in the domain-match selector (`domain_match='0'`) marks a fallback rule. At most one is allowed, and it runs after ordinary rules. Without a fallback or ordinary match, the complete current upstream list is the candidate group. Local resolver upstreams follow resolver-file order before custom upstreams, which follow configuration order. Only Strict Order tries that sequence one at a time; Dynamic Scheduling and Concurrent Query do not guarantee a local resolver wins first. Local records, valid cache, and HTTPS/SVCB bindings retain their respective behavior.
- Once an ordinary or fallback rule matches, only its referenced upstreams are used. An empty or entirely failed group does not automatically fall back to the full upstream list. Valid HTTPS/SVCB bindings can select upstreams through the bound rule. Valid DNS error-code responses can also win and are not transport failures.
- Rules other than `nxdomain`/`zeroip` can select one or more upstreams and optionally specify nftables sets for IPv4/IPv6 results. Restarting or reloading the service clears referenced sets, which are dynamically populated again according to the rules.
- Rules can also add their selected upstream endpoint IPs to their nftables sets. Hostname endpoints, including DoH URL hosts, are resolved and periodically refreshed. New addresses are appended; old addresses are not automatically removed from shared sets. This supplies match criteria for transparent-proxy or routing rules; updns does not configure proxies or traffic routing itself and cannot independently guarantee prevention of DNS leaks. Endpoint hostnames use the system resolver, so evaluate their resolution path together with system DNS, routing, and firewall configuration.

    | Action | Behavior |
    |---|---|
    | `ipv4+ipv6` | Return IPv4 and IPv6 results |
    | `ipv4only` | Return only IPv4 results |
    | `ipv6only` | Return only IPv6 results |
    | `nxdomain` | Return NXDOMAIN directly, without upstream queries or caching |
    | `zeroip` | Return `0.0.0.0` for A and `::` for AAAA, without upstream queries or caching |

- Rule-generated `nxdomain`/`zeroip` responses bypass cache reads, writes, and periodic prefetch, and are not written to nftables sets. Zero-IP records have TTL 60 seconds; rule NXDOMAIN has no SOA. CLI/log paths remain `NXDOMAIN RULE` or `ZERO-IP RULE`, never cache hits. Upstream NXDOMAIN retains normal negative-cache behavior.
- `ipv4+ipv6`, `ipv4only`, and `ipv6only` filter only A/AAAA records. MX, TXT, NS, SOA, SRV, PTR, other non-address records, and CNAME remain unchanged. HTTPS/SVCB records still return, retaining parameters such as `alpn` and `ech`.
- **HTTPS/SVCB handling**
    - **Address hints:** Filter `ipv4hint`/`ipv6hint` according to the action (for example, `ipv4only` removes `ipv6hint`). Retained hint addresses are added to the rule's sets just like A/AAAA addresses.
    - **Target binding:** Bind the target hostname (TargetName, or the query name itself for ServiceMode `.`) to the rule producing the response. Later client queries for that target use the bound rule's upstreams and action; addresses are added to all bound rules' sets and the target's own matched rule's sets for consistent nftables routing. When several rules bind a target, the first bound rule supplies upstreams and action.
    - **Binding lifecycle:** Bindings expire with record TTL, with a minimum of 30 seconds. Capacity equals `cache_size`; when full, new HTTPS/SVCB records are dropped instead of evicting valid bindings. Target addresses are prefetched asynchronously, without synchronous additional queries.
    - **Limits:** Drop an HTTPS/SVCB record if its target matches `nxdomain`/`zeroip`, or the chain including CNAME exceeds 16 hops. Queries without a matched rule, using the full default upstream group, do not establish bindings. Error-code responses return unchanged but establish no bindings and add no hints to nftables sets.

### DNS Auditing — CLI

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

- With the service running, CLI queries its local control interface, using the service's rules/cache and reporting nftables membership. Without the service, CLI resolves independently without service cache. By default it performs separate A and AAAA queries.
- `Local Answer` means hosts/DHCP data; `Cache Hit` means valid cache; `Prefetch Hit` means waiting for and reusing prefetch; `Upstream Query` means foreground resolution. `Upstream` reports each type separately, such as `A=local-dhcp / AAAA=local-hosts`, or `(none)` when absent. Cache hits show the cached origin, not a newly queried upstream. Local ANY answers combining both sources show `local-hosts+local-dhcp`.
- Failures still produce a complete report: `Response` shows `ERROR`, with an `ERRORS` section listing short normalized reasons per type, such as `Upstream timeout`, `Upstream connection refused`, `Upstream host unresolvable`, `Network not ready`, `No upstream configured`, or `All upstreams failed`. The command exits nonzero when both types fail. If only A or AAAA fails, the other result still appears; any successful type makes the exit code 0.
- Borders use the actual terminal column count, without minimum/maximum width limits. When terminal size cannot be read, `COLUMNS` is used, otherwise 48 columns. Text aligns by Unicode display width and wraps at whole grapheme boundaries, with continuations aligned to the value start. Narrow terminals put labels and values on separate lines. Extremely narrow terminals reduce indentation and replace unfit double-width graphemes with `?`. ANSI color/link escapes are removed from dynamic text, and control characters become spaces. Emoji and ambiguous-width characters can still differ between terminals.

## Advanced Technical Details

### Local Resolution

updns reads `/etc/hosts`, hosts files under `/tmp/hosts/`, and DHCP leases directly, supporting A, AAAA, PTR, and ANY. The first dnsmasq section of `/etc/config/dhcp` supplies the local domain and paths through `domain`, `expandhosts`, `leasefile`, `addnhosts`, and `hostsdir`; odhcpd `hostsdir` is also included. Without OpenWrt DHCP configuration, the default lease path is `/tmp/dhcp.leases`, the suffix is `lan`, and hosts short names are not automatically expanded.

- Local records take precedence over old upstream cache. Hosts override DHCP only for the same name and address family: IPv6 from hosts can coexist with IPv4 from DHCP. Expired leases are not used.
- Queries check for changes to files, directories, and local-domain settings. Answers use TTL 0, without DNS caching or periodic prefetch; logs and CLI show `Local Answer`.
- Local data does not bypass NXDOMAIN/Zero-IP blocking, address-family filters, or nftset rules.
- Unreadable or unparseable local data produces rate-limited warnings, with fallback to cache/upstream resolution. `use_local_resolv` only controls importing upstream addresses from resolv.conf, independently of local-record resolution.

### Data Structures and Performance

The cache uses a hash table and three indexed min-heaps: LFU eviction priority, refresh scheduling time, and ready-task expiration. Hit-priority updates, insertion/deletion, and individual scheduling are O(log N); idle scanning is O(1). Entries enter the ready queue only at state transitions, avoiding a complete scan, sort, and DNS-record copy every 100 ms. All indexes remove entries synchronously with deletion, without unbounded stale-node accumulation. Total space is O(N), with the in-flight map proportional to actual shared resolutions.

## Basic UCI Example

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

See [`openwrt/updns/root/etc/config/updns`](../openwrt/updns/root/etc/config/updns) for the full packaged configuration. Log levels are `debug`, `info`, `warn`, and `error`; per-query DNS logs also require `log_query`.

## LuCI Product and Frontend Reference

The entry is **Services → Turnout DNS**, at `/admin/services/updns`. The page has Running Status, Settings and Port, Upstream DNS, Domain Lists, Cache, Logging, and Rules areas.

### Page and Configuration Model

| Area | UCI section / fields | Behavior |
|---|---|---|
| Running Status | Service status and cache status file | Display service state and cache count, with periodic refresh |
| Settings and Port | `updns.general`: `enabled`, `port`, `use_local_resolv`, `local_resolv_file`, `query_strategy` | Service control, listening port, local resolvers, and upstream strategy |
| Upstream DNS | `updns.upstream`: `name`, `address`, `protocol`, `timeout` | Manage UDP/TCP/DoT/DoH upstreams; custom entries can be reordered, imported local entries stay at the top and cannot be edited, deleted, or dragged |
| Domain Lists | `updns.domainlist`: `name`, `file` | Upload/manage files referenced by rules |
| Cache | `updns.general`: `cache_enabled`, `cache_size`, `cache_prefetch` | Cache enablement, capacity, and active prefetch |
| Logging | `updns.general`: `log_query`, `log_level` | Query logging and severity |
| Rules | `updns.rule`: `name`, `domain_match`, `domain_lists`, `upstreams`, `action`, `ipv4_nftsets`, `ipv6_nftsets`, `add_upstream_ips` | Matching, upstreams, return types, and nftables handling |

Fresh installs default to `enabled=0`: the process does not start, and boot autostart is disabled. Other defaults: port `153`, local resolver import enabled, Concurrent Query (`query_strategy=2`), cache enabled with capacity `2048`, active prefetch enabled, and query logging disabled.

The rule editor's blocking selector (no blocking / NXDOMAIN / Zero-IP) is a UI control for `action`, not a separate UCI field. NXDOMAIN sets `action='nxdomain'`; Zero-IP sets `action='zeroip'`. Selecting either hides and clears upstreams, nftables sets, and address-family return options.

A rule with **Do not match (fallback rule)** (`domain_match='0'`) becomes the fallback. Only one is allowed; it runs after all ordinary rules fail to match. LuCI keeps it after ordinary rules.

### Frontend Files and Maintenance

| Content | Source |
|---|---|
| Page view, forms, and status | [`config.js`](../openwrt/luci-app-updns/root/www/luci-static/resources/view/updns/config.js) |
| Menu route | [`menu.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/luci/menu.d/luci-app-updns.json) |
| rpcd/ubus permissions | [`acl.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/rpcd/acl.d/luci-app-updns.json) |
| Simplified Chinese | [`updns.po`](../openwrt/luci-app-updns/po/zh_Hans/updns.po) |
| Packaged backend UCI | [`updns`](../openwrt/updns/root/etc/config/updns) |
| Package metadata and dependencies | [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile), [`openwrt/updns/Makefile`](../openwrt/updns/Makefile) |

The page reads and saves UCI. Imported local upstreams appear as generated `local-resolv-N` entries. Global ordering determines the candidate sequence when neither an ordinary rule nor fallback applies; explicit rule upstreams retain their own reference order, with actual queries following the selected strategy. Service status comes from rpcd `service.list`, cache count from `/tmp/updns_cache_size`, and nftables sets from `nft -j list sets`. Domain-list files use rpcd file interfaces.

Generated and custom upstreams use the same columns and right-side drag/edit/delete controls. All three controls are disabled for generated entries, with tooltips identifying their automatic origin.

Upstream column titles remain, but the column-description row is hidden; editor dialogs retain labels and help. Above the list, only local resolver import information or read errors are shown, without sorting instructions.

Protocol badges use line icons and colored capsules: amber UDP, green TCP, royal-blue DoH, and deep-sea-blue DoT text on a pale-blue background. Text remains visible rather than relying on color alone. Icons are inline SVG without external resources; generated/custom entries share styles. Protocol values and selector options are unchanged.

When changing the page, check UCI fields, rpcd ACLs, and backend behavior together. ACLs authorize explicit file paths; new state files, file-access paths, or system commands require matching ACL updates. Visible text uses `_()` and corresponding PO translations. Form-area or tab-layout changes require checking mapping and sorting logic.

## Build, Installation, and Testing

### Local Development

Requires Go 1.21 or later. Run on a macOS/Linux development machine; the locally built binary is for development/checks and cannot be installed directly on the router.

```sh
make build
make test
make lint
```

The binary is `bin/updns`. `make test` enables race detection; `make lint` runs gofmt and go vet.

### OpenWrt APK

Use an SDK matching the target OpenWrt release. The build script targets an x86_64 SDK. Place the source and SDK on a Linux x86_64 build host, then run from the project root:

```sh
./scripts/build-openwrt.sh /path/to/openwrt-sdk
```

The script synchronizes feeds, builds `updns` and `luci-app-updns`, and generates the Simplified Chinese translation package through LuCI's build rules. For a same-version update, raise the relevant package version before rebuilding:

- Backend: `PKG_RELEASE` in [`openwrt/updns/Makefile`](../openwrt/updns/Makefile)
- LuCI and translation: `PKG_VERSION` or `PKG_RELEASE` in [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile)

APKs are under the SDK's `bin/packages/x86_64/`, in `packages/` and `luci/`. Use the actual output filenames.

The APKs are not signed with a trusted key. Copy all three to the device, then install:

```sh
apk add --allow-untrusted \
    /tmp/updns-<version>.apk \
    /tmp/luci-app-updns-<version>.apk \
    /tmp/luci-i18n-updns-zh-cn-<version>.apk
```

Alternatively, upload and install with [`deploy.sh`](../deploy.sh):

```sh
./deploy.sh ./artifacts/updns-<version>.apk \
    ./artifacts/luci-app-updns-<version>.apk \
    ./artifacts/luci-i18n-updns-zh-cn-<version>.apk \
    root@router-host
```

For test/development environments, see [`scripts/build-openwrt.sh`](../scripts/build-openwrt.sh) and the deployment script. Releases should use an SDK matching the target firmware and be verified on OpenWrt hardware.

The repository's `artifacts/` directory holds the latest successfully built and tested packages. Replace a component's old package only after its new package passes validation.

### GitHub Actions package repository

The GitHub Actions workflow builds for Go-compatible package architectures using the OpenWrt 25.12.6 SDK. The architecture-independent LuCI and Chinese translation packages are built once with the x86_64 SDK. Publication requires all builds to succeed; actual architecture availability depends on the workflow results. Other OpenWrt releases require validation and workflow changes. A successful build does not imply testing on a device.

Before the first release, set **Settings → Pages → Build and deployment → Source** to **GitHub Actions**. The workflow replaces the entire Pages site. Generate a persistent signing key locally, keep the private key secret and backed up, and provide the public key's SHA-256 through an independently trusted channel:

```sh
umask 077
openssl ecparam -name prime256v1 -genkey -noout -out updns-private.pem
openssl pkey -in updns-private.pem -pubout -out updns-public.pem
sha256sum updns-public.pem
```

Add the private key contents as the repository Actions secret `APK_SIGNING_KEY`, then merge the workflow into the default branch. The first push publishes the current versions; approve the workflow if required. Failed runs can be rerun. A changed signing key is rejected to avoid silently breaking users' trust.

Default-branch pushes and manual runs compare the versions in the backend and LuCI Makefiles with the published versions. The first release builds all packages; unchanged versions are tested but not published. When only one package group changes, the other group's previously published packages are retained. Changed versions must be strictly greater; Git tags are not required. The translation package version includes LuCI's release number. When another OpenWrt release is added, previously published feeds are retained, subject to GitHub Pages storage limits.

After publication, verify the downloaded key's SHA-256 against the independently provided value before installing it and adding the matching feed:

```sh
ARCH="$(apk --print-arch)"
wget -O /tmp/updns-public.pem https://garinasset.github.io/updns/updns.pem
sha256sum /tmp/updns-public.pem
# Only after verifying the expected SHA-256:
cp /tmp/updns-public.pem /etc/apk/keys/updns.pem
echo "https://garinasset.github.io/updns/25.12.6/${ARCH}/packages.adb" >> /etc/apk/repositories.d/customfeeds.list
apk update
apk add updns luci-app-updns luci-i18n-updns-zh-cn
```

Do not use `--allow-untrusted` for this repository. Keep the official feeds matching your firmware enabled for dependencies, and recheck the feed after changing firmware or architecture. The remote repository is separate from local `artifacts/`.

## Troubleshooting

- **Service not running:** Check `general.enabled` in `/etc/config/updns`, inspect `logread -e updns`, and ensure the listening port is free.
- **Upstream query failure:** Check address, protocol, connectivity, and device CA certificates; set `log_level='debug'` for details.
- **Cache not working:** Check `cache_enabled=1`. `nxdomain`/`zeroip` rules answer directly without upstream queries, cache reads/writes, or prefetch. Upstream NXDOMAIN is stored only when eligible for negative caching.
- **nftables updates failing:** Ensure `nftables-json` is installed, target sets exist, and set/address families match.
- **LuCI cannot read data:** Check required UCI/file/command permissions in rpcd ACLs, then reload rpcd/LuCI.

procd collects regular updns logs for `logread`. Per-query DNS logs also require `log_query`.

Valid winning DNS responses, including upstream `SERVFAIL`/`REFUSED` and negative answers without SOA, do not generate query-failure warnings. With `log_query` enabled, regular query logs retain the actual RCODE. Only when no upstream produces a valid final response (timeout, question mismatch, referral, truncation, CNAME completion failure, etc.) do ordinary queries use rate-limited warn logs with per-upstream reasons. HTTPS/SVCB, PTR, and network-not-ready failures use debug logs.

Per-query logs use `QUERY [CLI]` or `QUERY [SRV]`, then domain, query type, matched rule, query path, and RCODE. Unmatched rules show `-`. Server queries append `@client-address` when available and `#network-prefix` for EDNS Client Subnet (ECS). Missing fields are omitted; CLI has no client/ECS placeholders. dnsmasq can supply ECS with `add_subnet`.

## Project Structure

| Area | Location | Purpose |
|---|---|---|
| Entry point and CLI | `cmd/updns/` | Service startup, query commands, and output |
| Configuration, rules, upstreams, cache | `config/`, `rules/`, `resolver/`, `cache/` | UCI parsing, matching, DNS queries, and caching |
| DNS service and control interface | `server/` | Requests, control interface, status, and cache logic |
| nftables and logging | `nftset/`, `logging/` | Set operations and logs |
| OpenWrt packages and LuCI | `openwrt/` | Init, packaged UCI, UI, ACLs, and translations |
| Build and deployment | `scripts/build-openwrt.sh`, `deploy.sh`, `Makefile` | Local builds, SDK packaging, and installation |

## License

[Apache License 2.0](../LICENSE)
