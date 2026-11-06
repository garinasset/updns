'use strict';
'require view';
'require form';
'require rpc';
'require uci';
'require ui';
'require fs';
'require poll';

var callServiceList = rpc.declare({
	object: 'service',
	method: 'list',
	params: [ 'name' ],
	expect: {}
});

var callCacheSize = rpc.declare({
	object: 'file',
	method: 'read',
	params: [ 'path' ],
	expect: { data: '' }
});

var statusPollRegistered = false;
var domainListToggleRegistered = false;
var originalUciMove;
var NO_MATCH = '__no_match__';

function parseLocalResolvers(content, configuredUpstreams) {
	var addresses = {};
	var resolvers = [];
	var index = 0;

	configuredUpstreams.forEach(function(upstream) {
		if (upstream.address)
			addresses[upstream.address] = true;
	});

	String(content || '').split(/\r?\n/).forEach(function(line) {
		var fields = line.trim().split(/\s+/);
		if (fields.length !== 2 || fields[0] !== 'nameserver' || addresses[fields[1]])
			return;

		addresses[fields[1]] = true;
		index++;
		resolvers.push({
			name: 'local-resolv-' + index,
			address: fields[1]
		});
	});

	return resolvers;
}

function addMultiValues(option, values) {
	values.forEach(function(item) {
		option.value(item.value, item.label);
	});
}

function addMultiValueOption(section, key, title, description, values) {
	var option = section.option(
		values.length ? form.MultiValue : form.DummyValue,
		key, title, description
	);

	if (values.length)
		addMultiValues(option, values);
	else
		option.default = _('No options available');

	return option;
}

function addDynamicMultiValueOption(section, key, title, description, getValues, refreshers) {
	var option = section.option(form.MultiValue, key, title, description);
	var placeholder = option.placeholder;
	var renderWidget = option.renderWidget;

	refreshers.push(function() {
		var values = getValues();
		option.keylist = [];
		option.vallist = [];
		addMultiValues(option, values);
		option.placeholder = values.length ? placeholder : _('No options available');
	});

	option.renderWidget = function() {
		if (!this.keylist || !this.keylist.length)
			return E('span', { 'class': 'cbi-value-description' }, _('No options available'));

		return renderWidget.apply(this, arguments);
	};

	return option;
}

function listDomainLists() {
	var lists = (uci.sections('updns', 'domainlist') || []).map(function(section) {
		var path = section.file || section.path || '';
		var name = section.name || section['.name'];
		return { value: name, label: name + ' (' + path + ')' };
	});

	lists.unshift({ value: NO_MATCH, label: _('Do Not Match (Fallback Rule)') });
	return lists;
}

function getDomainListValues(value) {
	if (Array.isArray(value))
		return value;

	return value ? [ value ] : [];
}

function listDomainListOwners(exceptSectionID) {
	var owners = {};

	(uci.sections('updns', 'rule') || []).forEach(function(rule) {
		if (rule['.name'] === exceptSectionID)
			return;

		var ruleName = rule.name || rule['.name'];
		if (isFallbackRule(rule))
			owners[NO_MATCH] = owners[NO_MATCH] || ruleName;
		getDomainListValues(rule.domain_lists).forEach(function(name) {
			owners[name] = owners[name] || ruleName;
		});
	});

	return owners;
}

function markUnavailableDomainLists(frame, sectionID) {
	var owners = listDomainListOwners(sectionID);

	frame.querySelectorAll('.cbi-dropdown li[data-value]').forEach(function(item) {
		var owner = owners[item.dataset.value];
		if (!owner)
			return;

		item.classList.add('updns-domain-list-unavailable');
		item.setAttribute('aria-disabled', 'true');
		item.style.opacity = '0.55';
		item.style.pointerEvents = 'none';
		item.title = _('Already used by rule: %s').format(owner);

		var checkbox = item.querySelector('input[type="checkbox"]');
		if (checkbox)
			checkbox.disabled = true;

		var label = item.querySelector('label');
		if (label)
			label.textContent += ' - ' + item.title;
	});
}

function isFallbackRule(rule) {
	return rule.domain_match === '0';
}

function moveFallbackRuleToLast() {
	var rules = uci.sections('updns', 'rule') || [];
	var fallback = rules.find(isFallbackRule);

	if (fallback && rules[rules.length - 1] !== fallback)
		originalUciMove.call(uci, 'updns', fallback['.name'],
			rules[rules.length - 1]['.name'], true);
}

function installRuleOrderGuard() {
	if (originalUciMove)
		return;

	originalUciMove = uci.move;
	uci.move = function(config) {
		var result = originalUciMove.apply(this, arguments);
		if (config === 'updns')
			moveFallbackRuleToLast();
		return result;
	};
}

function updateFallbackDragHandles(root) {
	if (!root)
		return;

	var section = root.querySelector('#cbi-updns-rule');
	if (!section)
		return;

	var rules = uci.sections('updns', 'rule') || [];
	var rows = Array.from(section.querySelectorAll('tbody tr')).filter(function(row) {
		return row.querySelector('.drag-handle');
	});

	rules.forEach(function(rule, index) {
		var handle = rows[index] && rows[index].querySelector('.drag-handle');
		if (!handle)
			return;

		handle.disabled = isFallbackRule(rule);
		handle.title = isFallbackRule(rule)
			? _('Fallback rules are always kept last.')
			: _('Drag to reorder');
	});
}

function isLocalAnswerAction(value) {
	return value === 'nxdomain' || value === 'zeroip';
}

function updateRuleActionFrames(modal) {
	if (!modal)
		return;

	var block = modal.querySelector('.cbi-value[data-name="block"] select');
	var blocked = !!block && block.value !== 'none';

	[ 'upstreams', 'ipv4_nftsets', 'ipv6_nftsets', 'add_upstream_ips', 'action' ].forEach(function(name) {
		var frame = modal.querySelector('.cbi-value[data-name="' + name + '"]');
		if (frame)
			frame.style.display = blocked ? 'none' : '';
	});
}

function getActiveRuleModal() {
	return Array.from(document.querySelectorAll('.modal')).find(function(frame) {
		return getComputedStyle(frame).visibility !== 'hidden' &&
			frame.querySelector('.cbi-value[data-name="action"] select');
	});
}

function isNXDomainSelected() {
	var modal = getActiveRuleModal();
	var block = modal && modal.querySelector('.cbi-value[data-name="block"] select');
	return !!block && block.value !== 'none';
}

function hideAndClearOptionForNXDomain(option, key) {
	var renderFrame = option.renderFrame;
	option.renderFrame = function(section_id, in_table) {
		var frame = renderFrame.apply(this, arguments);
		if (!in_table && isLocalAnswerAction(uci.get('updns', section_id, 'action')))
			frame.style.display = 'none';
		return frame;
	};

	var write = option.write;
	option.write = function(section_id) {
		if (isNXDomainSelected()) {
			uci.unset('updns', section_id, key);
			return Promise.resolve();
		}

		return write ? write.apply(this, arguments) : Promise.resolve();
	};
}

function installDomainListToggle() {
	if (domainListToggleRegistered)
		return;

	document.addEventListener('change', function(event) {
		var widget = event.target;
		if (!widget.matches)
			return;

		var modal = widget.closest('.modal');
		if (!modal)
			return;

		if (widget.matches('.cbi-value[data-name="block"] select'))
			updateRuleActionFrames(modal);
	});
	document.addEventListener('cbi-dropdown-change', function(event) {
		var node = event.target;
		if (!node.closest || !node.closest('.cbi-value[data-name="domain_lists"]'))
			return;

		var frame = node.closest('.cbi-value[data-name="domain_lists"]');
		var instance = event.detail && event.detail.instance;
		if (!instance || instance.noMatchBusy)
			return;

		var values = getDomainListValues(instance.getValue());
		var hadNoMatch = frame.dataset.hadNoMatch === '1';
		var hasNoMatch = values.indexOf(NO_MATCH) !== -1;

		if (hasNoMatch && values.length > 1) {
			instance.noMatchBusy = true;
			values = hadNoMatch
				? values.filter(function(value) { return value !== NO_MATCH; })
				: [ NO_MATCH ];
			instance.setValue(values);
			instance.noMatchBusy = false;
			hasNoMatch = values.indexOf(NO_MATCH) !== -1;
		}

		frame.dataset.hadNoMatch = hasNoMatch ? '1' : '0';
	});
	document.addEventListener('reset', function(event) {
		var modal = event.target.closest && event.target.closest('.modal');
		if (!modal)
			return;

		setTimeout(function() {
			updateRuleActionFrames(modal);
		}, 0);
	});
	domainListToggleRegistered = true;
}

function listConfiguredUpstreams(localResolvers) {
	var upstreams = (uci.sections('updns', 'upstream') || []).map(function(section) {
		var name = section.name || section['.name'];
		return { value: name, label: name + ' (' + (section.address || '') + ')' };
	});

	localResolvers.forEach(function(upstream) {
		upstreams.push({
			value: upstream.name,
			label: upstream.name + ' (' + upstream.address + ')'
		});
	});

	return upstreams;
}

function listNFTSets(reply) {
	var document;
	var sets = [];
	var seen = {};

	try {
		document = JSON.parse(reply && reply.stdout || '');
	}
	catch (error) {
		return { values: [], error: _('The system nftables set list could not be parsed.') };
	}

	(document.nftables || []).forEach(function(item) {
		var set = item.set;
		if (!set || !set.family || !set.table || !set.name)
			return;

		var value = set.family + '/' + set.table + '/' + set.name;
		if (seen[value])
			return;

		seen[value] = true;
		sets.push({ value: value, label: set.table + '/' + set.name + ' (' + set.family + ')' });
	});

	sets.sort(function(a, b) {
		return a.label.localeCompare(b.label);
	});

	return { values: sets, error: null };
}

function ruleBadge(kind, text, missing) {
	return E('span', {
		'class': 'updns-badge updns-badge-' + kind + (missing ? ' updns-badge-missing' : '')
	}, text);
}

function ruleBadges(rows) {
	rows = rows.filter(function(row) { return row.length; });
	return rows.length ? E('div', { 'class': 'updns-badge-rows' }, rows.map(function(row) {
		return E('div', { 'class': 'updns-badges' }, row);
	})) : '—';
}

function ruleMatchBadges(section_id) {
	if (uci.get('updns', section_id, 'domain_match') === '0')
		return ruleBadges([ [ ruleBadge('fallback', _('Do Not Match (Fallback Rule)')) ] ]);

	var known = {};
	(uci.sections('updns', 'domainlist') || []).forEach(function(item) {
		known[item.name || item['.name']] = true;
	});

	return ruleBadges([ getDomainListValues(uci.get('updns', section_id, 'domain_lists')).map(function(name) {
		return ruleBadge('list', name, !known[name]);
	}) ]);
}

function ruleOperationBadges(section_id, upstreamNames, nftSets) {
	if (isLocalAnswerAction(uci.get('updns', section_id, 'action')))
		return ruleBadges([ [ ruleBadge('local', _('Local Answer')) ] ]);

	var dnsBadges = [], nftBadges = [], ipBadges = [];
	getDomainListValues(uci.get('updns', section_id, 'upstreams')).forEach(function(name) {
		dnsBadges.push(ruleBadge('upstream', 'DNS: ' + name, upstreamNames.indexOf(name) < 0));
	});
	[ [ 'ipv4_nftsets', 'NFT v4: ' ], [ 'ipv6_nftsets', 'NFT v6: ' ] ].forEach(function(item) {
		getDomainListValues(uci.get('updns', section_id, item[0])).forEach(function(value) {
			nftBadges.push(ruleBadge('nft', item[1] + value.split('/').slice(1).join('/'),
				nftSets.indexOf(value) < 0));
		});
	});
	if (uci.get('updns', section_id, 'add_upstream_ips') === '1')
		ipBadges.push(ruleBadge('upstreamip', _('Upstream DNS IPs → NFT')));

	return ruleBadges([ dnsBadges, nftBadges, ipBadges ]);
}

function ruleReturnBadge(section_id) {
	var action = uci.get('updns', section_id, 'action') || 'ipv4+ipv6';
	var texts = {
		'nxdomain': _('NXDOMAIN Response'),
		'zeroip': _('Zero-IP Response'),
		'ipv4+ipv6': _('IPv4 + IPv6'),
		'ipv4only': _('IPv4 Only'),
		'ipv6only': _('IPv6 Only')
	};

	return ruleBadges([ [ ruleBadge('return', texts[action] || action) ] ]);
}

function isEmptyValue(value) {
	if (Array.isArray(value))
		return value.length === 0;

	return value == null || String(value).trim() === '';
}

function requireValue(option, message, skip) {
	var validate = option.validate;

	option.optional = true;
	option.validate = function(section_id, value) {
		if (isEmptyValue(value))
			return skip && skip() ? true : message;

		return validate ? validate.apply(this, arguments) : true;
	};
}

function addRuleColumn(section, key, title, render) {
	var option = section.option(form.DummyValue, key, title);
	option.modalonly = false;
	option.textvalue = render;
	return option;
}

function installRuleStyles() {
	if (document.getElementById('updns-rule-styles'))
		return;

	var colors = {
		list: [ '#F1F3F5', '#495057' ], fallback: [ 'rgba(230,126,34,.15)', 'inherit', 'rgba(230,126,34,.55)' ],
		upstream: [ '#E0E7FF', '#3730A3' ], nft: [ '#FFF1DB', '#996000' ],
		upstreamip: [ '#FEF9C3', '#854D0E' ], local: [ '#E0F2FE', '#0369A1' ], 'return': [ '#E6F4EA', '#267342' ]
	};
	var css = [
		'.updns-badge-rows{display:flex;flex-direction:column;gap:4px;align-items:flex-start}',
		'.updns-badges{display:flex;flex-wrap:wrap;gap:4px}',
		'.updns-badge{display:inline-block;padding:1px 8px;border:1px solid transparent;border-radius:10px;font-size:90%;line-height:1.6;white-space:nowrap}',
		'.updns-badge-missing{border-color:#e74c3c !important;background:#fde8e6 !important;color:#c0392b !important;text-decoration:line-through}'
	];
	Object.keys(colors).forEach(function(kind) {
		var c = colors[kind];
		css.push('.updns-badge-' + kind + '{background:' + c[0] + ';color:' + c[1] +
			';border-color:' + (c[2] || c[0]) + '}');
	});
	document.head.appendChild(E('style', { 'id': 'updns-rule-styles' }, css.join('')));
}

function addRuntimeUpstreamRows(section, upstreams) {
	if (!upstreams.length)
		return;

	var body = section.querySelector('table tbody');
	if (!body)
		return;

	upstreams.slice().reverse().forEach(function(upstream) {
		var row = E('tr', { 'class': 'tr cbi-section-table-row updns-runtime-upstream' }, [
			E('td', { 'class': 'td cbi-value-field' }, upstream.name),
			E('td', { 'class': 'td cbi-value-field' }, upstream.address),
			E('td', { 'class': 'td cbi-value-field' }, 'UDP'),
			E('td', { 'class': 'td cbi-value-field' }, '5'),
			E('td', { 'class': 'td cbi-section-table-cell nowrap cbi-section-actions' }, _('Automatic'))
		]);
		body.insertBefore(row, body.firstChild);
	});
}

function formatUpstreamProtocols(section) {
	var labels = {
		udp: 'UDP',
		tcp: 'TCP',
		dot: 'DoT',
		doh: 'DoH'
	};

	section.querySelectorAll('tbody tr.cbi-section-table-row:not(.updns-runtime-upstream)').forEach(function(row) {
		var cell = row.cells[2];
		var protocol = cell && String(cell.textContent).trim().toLowerCase();
		if (labels[protocol])
			cell.textContent = labels[protocol];
	});
}

function addStatusStyles() {
	if (document.getElementById('updns-status-styles'))
		return;

	document.head.appendChild(E('style', { 'id': 'updns-status-styles' }, [
		'.updns-status-section{margin-bottom:18px}',
		'.updns-status-grid{display:grid;grid-template-columns:max-content max-content;gap:8px 10px;align-items:center}',
		'.updns-status-label,.updns-status-value{margin:0}',
		'.updns-status-icon{margin-right:6px;vertical-align:middle}',
		'.cbi-map-tabbed{margin-bottom:18px}',
	].join('')));
}

function arrangeTabbedMap(node, data, localResolvers) {
	var sections = Array.from(node.children).filter(function(child) {
		return child.classList.contains('cbi-section');
	});
	var service = data[1] && data[1].updns;
	var instances = service && service.instances || {};
	var running = Object.keys(instances).some(function(name) {
		return instances[name].running === true;
	});
	var enabled = uci.get('updns', 'general', 'enabled') === '1';
	var serviceUnavailable = !!(data[1] && data[1].error) || (enabled && !running);
	var cache = data[1] && !data[1].error
		? (running && data[2] && !data[2].error
			? String(data[2]).trim()
			: (running ? _('Unavailable') : '0'))
		: _('Unavailable');
	var status = E('div', { 'class': 'cbi-section updns-status-section' }, [
		E('h3', {}, _('Running Status')),
		E('div', { 'class': 'updns-status-grid' }, [
			E('p', { 'class': 'updns-status-label' }, _('Service Status')),
			E('p', { 'class': 'updns-status-value', 'id': 'updns-service-state' }, [
					E('span', {
						'class': 'updns-status-icon',
						'id': 'updns-service-icon',
						'aria-hidden': 'true'
					}, serviceUnavailable ? '⚪' : (running ? '🟢' : '🔴')),
					E('span', { 'id': 'updns-service-label' }, serviceUnavailable
						? _('Unavailable')
						: (running ? _('Running') : _('Stopped')))
			]),
			E('p', { 'class': 'updns-status-label' }, _('Cache Entries')),
			E('p', { 'class': 'updns-status-value' }, [
				E('span', { 'id': 'updns-cache-count' }, cache)
			])
		])
	]);
	var tabTitles = [
		_('Settings and Port'),
		_('Upstream DNS'),
		_('Domain Lists'),
		_('Cache'),
		_('Logging')
	];

	if (sections.length !== 6) {
		node.appendChild(E('p', { 'class': 'alert-message warning' },
			_('The configuration sections could not be arranged into tabs.')));
		return node;
	}

	addStatusStyles();
	addRuntimeUpstreamRows(sections[1], localResolvers);
	formatUpstreamProtocols(sections[1]);

	var tabGroup = E('div', { 'class': 'cbi-map-tabbed' });
	var panes = E('div', { 'class': 'cbi-map-tabbed-content' });
	var rules = sections[5];

	sections.slice(0, 5).forEach(function(section, index) {
		var pane = E('div', {
			'data-tab': 'updns-' + index,
			'data-tab-title': tabTitles[index]
		});
		section.remove();
		pane.appendChild(section);
		panes.appendChild(pane);
	});

	rules.remove();
	tabGroup.appendChild(panes);
	node.appendChild(status);
	node.appendChild(tabGroup);
	node.appendChild(rules);
	ui.tabs.initTabGroup(panes.childNodes);

	return node;
}

function refreshFallbackRuleControls(root) {
	updateFallbackDragHandles(root);
}

function updateRuntimeStatus(root) {
	if (!root)
		return Promise.resolve();

	return Promise.all([
		callServiceList('updns').catch(function(error) {
			return { error: error };
		}),
		callCacheSize('/tmp/updns_cache_size').catch(function(error) {
			return { error: error };
		})
	]).then(function(data) {
		var service = data[0] && data[0].updns;
		var instances = service && service.instances || {};
		var running = Object.keys(instances).some(function(name) {
			return instances[name].running === true;
		});
		var state = root.querySelector('#updns-service-state');
		var icon = root.querySelector('#updns-service-icon');
		var label = root.querySelector('#updns-service-label');
		var cache = root.querySelector('#updns-cache-count');
		var enabled = uci.get('updns', 'general', 'enabled') === '1';

		if (state && icon && label) {
			var unavailable = !!(data[0] && data[0].error) || (enabled && !running);
			icon.textContent = unavailable ? '⚪' : (running ? '🟢' : '🔴');
			label.textContent = unavailable
				? _('Unavailable')
				: (running ? _('Running') : _('Stopped'));
		}
		if (cache) {
			if (data[0] && data[0].error)
				cache.textContent = _('Unavailable');
			else if (!running)
				cache.textContent = '0';
			else
				cache.textContent = data[1] && !data[1].error
					? String(data[1]).trim()
					: _('Unavailable');
		}
	});
}

return view.extend({
	load: function() {
		var config = uci.load('updns').then(function() {
			if (uci.get('updns', 'general', 'use_local_resolv') !== '1')
				return { resolv: '', error: null };

			var path = uci.get('updns', 'general', 'local_resolv_file') ||
				'/tmp/resolv.conf.d/resolv.conf.auto';

			return rpc.declare({
				object: 'file',
				method: 'read',
				params: [ 'path' ],
				expect: { data: '' }
			})(path).then(function(reply) {
				return { resolv: reply || '', error: null };
			}).catch(function(error) {
				return { resolv: '', error: error };
			});
		});

		return Promise.all([
			config,
			callServiceList('updns').catch(function(error) {
				return { error: error };
			}),
			callCacheSize('/tmp/updns_cache_size').catch(function(error) {
				return { error: error };
			}),
			fs.exec('/usr/sbin/nft', [ '-j', 'list', 'sets' ]).catch(function(error) {
				return { error: error, stdout: '' };
			})
		]);
	},

	render: function(data) {
		installRuleOrderGuard();
		moveFallbackRuleToLast();

		var resolv = data[0] || {};
		var configuredUpstreams = uci.sections('updns', 'upstream') || [];
		var localResolvers = parseLocalResolvers(resolv.resolv, configuredUpstreams);
		var nftResult = listNFTSets(data[3]);
		var m = new form.Map('updns', _('UpDNS'),
			_('UpDNS is an upstream DNS provider that offers a unified DNS entry point and routes queries to selected upstream servers.'));
		var s, o;
		var dynamicOptionRefreshers = [];

		s = m.section(form.NamedSection, 'general', 'updns', _('Settings and Port'));
		o = s.option(form.Flag, 'enabled', _('Enable UpDNS'),
			_('Start or stop the UpDNS service.'));
		o.default = '0';
		o.rmempty = false;
		o = s.option(form.Value, 'port', _('Port'),
			_('UDP and TCP port on which UpDNS accepts DNS queries.'));
		o.datatype = 'port';
		o.default = '153';
		o.rmempty = false;
		o = s.option(form.Flag, 'use_local_resolv', _('Use Local Resolv File'),
			_('Add every nameserver in the resolver file below as a UDP upstream when the service starts.'));
		o.default = '1';
		o.rmempty = false;
		o = s.option(form.Value, 'local_resolv_file', _('Local Resolv File'),
			_('Path to a standard resolv.conf file. Its nameserver entries are loaded automatically.'));
		o.default = '/tmp/resolv.conf.d/resolv.conf.auto';
		o.depends('use_local_resolv', '1');
		o = s.option(form.ListValue, 'query_strategy', _('Upstream DNS Selection Strategy'),
			_('Defines how upstream DNS servers referenced by a rule are selected. Dynamic Scheduling dynamically selects and reuses well-performing upstreams; Strict Order tries them in the configured order; Concurrent Query uses the first valid response.'));
		o.value('0', _('Dynamic Scheduling'));
		o.value('1', _('Strict Order'));
		o.value('2', _('Concurrent Query'));
		o.default = '0';
		o.rmempty = false;

		s = m.section(form.GridSection, 'upstream', _('Upstream DNS'));
		s.addremove = true;
		s.anonymous = true;
		s.description = localResolvers.length
			? _('Nameservers automatically loaded from the local resolver file: %s').format(
				localResolvers.map(function(upstream) {
					return upstream.address + ' (' + upstream.name + ')';
				}).join(', '))
			: resolv.error
				? _('Could not read the local resolver file: %s').format(String(resolv.error))
				: _('Local resolver nameservers are added here automatically when enabled and available.');
		o = s.option(form.Value, 'name', _('Name'),
			_('Unique identifier used by rules to select this upstream.'));
		o.rmempty = false;
		o = s.option(form.Value, 'address', _('Address'),
			_('UDP/TCP default to port 53, DoT to 853, and HTTPS/DoH to 443. Use host:port for custom DNS ports (IPv6: [2001:db8::1]:5353); for DoH, enter a full URL such as https://dns.example:8443/dns-query.'));
		o.rmempty = false;
		o = s.option(form.ListValue, 'protocol', _('Protocol'),
			_('Transport protocol used to send DNS queries to this server.'));
		o.value('udp', 'UDP');
		o.value('tcp', 'TCP');
		o.value('dot', 'DoT');
		o.value('doh', 'DoH');
		o.default = 'doh';
		o.rmempty = false;
		o = s.option(form.Value, 'timeout', _('Timeout'),
			_('Maximum number of seconds to wait for a response from this upstream.'));
		o.datatype = 'uinteger';
		o.default = '5';
		o.rmempty = false;

		s = m.section(form.GridSection, 'domainlist', _('Domain Lists'));
		s.addremove = true;
		s.anonymous = true;
		o = s.option(form.Value, 'name', _('Domain List Name'),
			_('A unique identifier shown in rule selectors.'));
		o.rmempty = false;
		o.validate = function(section_id, value) {
			var name = String(value || '').trim();
			if (!name)
				return _('A domain list name is required.');

			var duplicate = (uci.sections('updns', 'domainlist') || []).some(function(section) {
				return section['.name'] !== section_id &&
					String(section.name || '').trim().toLowerCase() === name.toLowerCase();
			});
			return duplicate ? _('Domain list names must be unique.') : true;
		};
		o = s.option(form.FileUpload, 'file', _('Domain List File'),
			_('Upload a plain-text file with one domain per line. Rules can select the saved list.'));
		o.root_directory = '/etc/updns/domainlists';
		o.enable_upload = true;
		o.enable_delete = true;
		o.datatype = 'file';
		o.rmempty = false;

		s = m.section(form.NamedSection, 'general', 'updns', _('Cache'));
		o = s.option(form.Flag, 'cache_enabled', _('Enable Cache'),
			_('Enable or disable DNS caching.'));
		o.default = '1';
		o.rmempty = false;
		o = s.option(form.Value, 'cache_size', _('Cache Size'),
			_('Maximum number of DNS entries kept in the cache.'));
		o.datatype = 'uinteger';
		o.default = '4096';
		o.depends('cache_enabled', '1');
		o = s.option(form.Flag, 'active_preload', _('Active Preload'),
			_('Refresh cached records shortly before their TTL expires.'));
		o.default = '1';
		o.depends('cache_enabled', '1');

		s = m.section(form.NamedSection, 'general', 'updns', _('Logging'));
		o = s.option(form.Flag, 'log_query', _('Log Queries'),
			_('Write individual DNS queries to the system log. Disabled by default.'));
		o.default = '0';
		o.rmempty = false;
		o = s.option(form.ListValue, 'log_level', _('Log Level'),
			_('Minimum severity written to stdout/stderr and the system log.'));
		o.value('debug', _('Debug'));
		o.value('info', _('Info'));
		o.value('warn', _('Warning'));
		o.value('error', _('Error'));
		o.default = 'info';

		s = m.section(form.GridSection, 'rule', _('Rules'));
		s.addremove = true;
		s.anonymous = true;
		s.sortable = true;
		s.description = _('Drag domain-matching rules to change their evaluation order. The first matching rule is applied. A fallback rule is always kept at the bottom and is used only when no domain-matching rule matches.');
		var upstreamNames = listConfiguredUpstreams(localResolvers).map(function(item) { return item.value; });
		var nftValues = nftResult.values.map(function(item) { return item.value; });
		installRuleStyles();
		addRuleColumn(s, '_col_name', _('Name'), function(section_id) {
			return E('strong', {}, uci.get('updns', section_id, 'name') || section_id);
		});
		addRuleColumn(s, '_col_match', _('Rule Match Conditions'), ruleMatchBadges);
		addRuleColumn(s, '_col_operation', _('Rule Operations'), function(section_id) {
			return ruleOperationBadges(section_id, upstreamNames, nftValues);
		});
		addRuleColumn(s, '_col_return', _('Rule Return Result'), ruleReturnBadge);
		o = s.option(form.Value, 'name', _('Rule Name'),
			_('A short name shown in logs and CLI output.'));
		o.rmempty = false;
		o.modalonly = true;
		o = addDynamicMultiValueOption(s, 'domain_lists', _('If the Domain Matches'),
			_('Select one or more domain lists. A match in any selected list triggers this rule. Select "Do Not Match (Fallback Rule)" to skip domain matching; the rule then becomes the only fallback rule and applies last, when no other rule matches. It cannot be combined with domain lists.'),
			listDomainLists, dynamicOptionRefreshers);
		o.rmempty = false;
		o.modalonly = true;
		o.forcewrite = true;
		o.cfgvalue = function(section_id) {
			if (uci.get('updns', section_id, 'domain_match') === '0')
				return [ NO_MATCH ];
			return uci.get('updns', section_id, 'domain_lists');
		};
		o.write = function(section_id, value) {
			var values = getDomainListValues(value);
			if (values.indexOf(NO_MATCH) !== -1) {
				uci.set('updns', section_id, 'domain_match', '0');
				uci.unset('updns', section_id, 'domain_lists');
			}
			else {
				uci.set('updns', section_id, 'domain_match', '1');
				uci.set('updns', section_id, 'domain_lists', values);
			}
			return Promise.resolve();
		};
		o.remove = function() {
			return Promise.resolve();
		};
		var domainListsTextvalue = o.textvalue;
		o.textvalue = function(section_id) {
			if (uci.get('updns', section_id, 'domain_match') === '0')
				return _('Do Not Match (Fallback Rule)');
			return domainListsTextvalue.apply(this, arguments);
		};
		var renderDomainListsFrame = o.renderFrame;
		o.renderFrame = function(section_id) {
			var frame = renderDomainListsFrame.apply(this, arguments);
			markUnavailableDomainLists(frame, section_id);
			frame.dataset.hadNoMatch =
				uci.get('updns', section_id, 'domain_match') === '0' ? '1' : '0';
			return frame;
		};
		o.validate = function(section_id, value) {
			var selected = getDomainListValues(value);
			var current = (uci.sections('updns', 'rule') || []).find(function(rule) {
				return rule['.name'] === section_id;
			});
			var currentValues = current && isFallbackRule(current)
				? [ NO_MATCH ]
				: getDomainListValues(current && current.domain_lists);
			var owners = listDomainListOwners(section_id);

			if (selected.indexOf(NO_MATCH) !== -1 && selected.length > 1)
				return _('"Do Not Match (Fallback Rule)" cannot be combined with domain lists.');

			for (var i = 0; i < selected.length; i++) {
				var owner = owners[selected[i]];
				if (owner && selected[i] === NO_MATCH && currentValues.indexOf(NO_MATCH) === -1)
					return _('Only one fallback rule is allowed.');
				if (owner && currentValues.indexOf(selected[i]) === -1)
					return _('Domain list %s is already used by rule %s.').format(selected[i], owner);
			}

			return true;
		};
		o = s.option(form.ListValue, 'block', _('Block'),
			_('Answer matching domains locally without querying upstream DNS. NXDOMAIN Response returns a non-existent domain; Zero-IP Response returns 0.0.0.0 for A and :: for AAAA.'));
		o.modalonly = true;
		o.value('none', _('No Blocking'));
		o.value('nxdomain', _('NXDOMAIN Response'));
		o.value('zeroip', _('Zero-IP Response'));
		o.default = 'none';
		o.rmempty = false;
		o.cfgvalue = function(section_id) {
			var action = uci.get('updns', section_id, 'action');
			return isLocalAnswerAction(action) ? action : 'none';
		};
		o.forcewrite = true;
		o.write = function(section_id, formvalue) {
			if (formvalue !== 'none') {
				uci.set('updns', section_id, 'action', formvalue);
				return Promise.resolve();
			}

			var modal = getActiveRuleModal();
			var action = modal && modal.querySelector('.cbi-value[data-name="action"] select');
			uci.set('updns', section_id, 'action', action ? action.value : 'ipv4+ipv6');
			return Promise.resolve();
		};
		o = addDynamicMultiValueOption(s, 'upstreams', _('Then Query Upstream DNS'),
			_('Select the upstream servers used for matching domains (required). No upstream is needed when Block is set.'),
			function() {
				return listConfiguredUpstreams(localResolvers);
			}, dynamicOptionRefreshers);
		o.modalonly = true;
		requireValue(o, _('Select at least one upstream DNS.'), isNXDomainSelected);
		hideAndClearOptionForNXDomain(o, 'upstreams');
		o = addMultiValueOption(s, 'ipv4_nftsets', _('Add IPv4 Results to NFT Sets'),
			_('Select system nftables sets that receive resolved IPv4 addresses.'),
			nftResult.values);
		o.modalonly = true;
		hideAndClearOptionForNXDomain(o, 'ipv4_nftsets');
		o = addMultiValueOption(s, 'ipv6_nftsets', _('Add IPv6 Results to NFT Sets'),
			_('Select system nftables sets that receive resolved IPv6 addresses.'),
			nftResult.values);
		o.modalonly = true;
		hideAndClearOptionForNXDomain(o, 'ipv6_nftsets');
		if (nftResult.error) {
			o.description = _('Could not list system nftables sets: %s').format(String(nftResult.error));
		}
		o = s.option(form.Flag, 'add_upstream_ips', _('Add Upstream DNS IPs to NFT Sets'),
			_("Also add the selected upstream servers' IPv4 and IPv6 addresses to the corresponding sets. Hostnames are resolved at startup and refreshed every five minutes."));
		o.default = '0';
		o.rmempty = false;
		o.modalonly = true;
		hideAndClearOptionForNXDomain(o, 'add_upstream_ips');
		o = s.option(form.ListValue, 'action', _('And Return'),
			_('Choose which address records are returned to the client.'));
		o.value('ipv4+ipv6', _('IPv4 + IPv6'));
		o.value('ipv4only', _('IPv4 Only'));
		o.value('ipv6only', _('IPv6 Only'));
		o.default = 'ipv4+ipv6';
		o.modalonly = true;
		o.cfgvalue = function(section_id) {
			var action = uci.get('updns', section_id, 'action');
			return isLocalAnswerAction(action) || !action ? 'ipv4+ipv6' : action;
		};
		o.textvalue = function(section_id) {
			var action = uci.get('updns', section_id, 'action');
			if (action === 'nxdomain')
				return _('NXDOMAIN Response');
			if (action === 'zeroip')
				return _('Zero-IP Response');
			var idx = this.keylist.indexOf(action || 'ipv4+ipv6');
			return idx >= 0 ? this.vallist[idx] : action;
		};
		var writeAction = o.write;
		o.write = function(section_id, formvalue) {
			var modal = getActiveRuleModal();
			var block = modal && modal.querySelector('.cbi-value[data-name="block"] select');
			if (block && block.value !== 'none') {
				[ 'upstreams', 'ipv4_nftsets', 'ipv6_nftsets', 'add_upstream_ips' ]
					.forEach(function(key) {
						uci.unset('updns', section_id, key);
					});
				uci.set('updns', section_id, 'action', block.value);
				return Promise.resolve();
			}

			return writeAction.call(this, section_id, formvalue);
		};

		var handleRuleModalSave = s.handleModalSave;
		s.handleModalSave = function() {
			var modal = getActiveRuleModal();
			var nameWidget = modal && modal.querySelector('.cbi-value[data-name="name"] input');
			var block = modal &&
				modal.querySelector('.cbi-value[data-name="block"] select');
			var sectionID = nameWidget && nameWidget.id.split('.')[3];
			var clearIPOptions = block && block.value !== 'none';

			return Promise.resolve(handleRuleModalSave.apply(this, arguments)).then(function(result) {
				if (clearIPOptions && sectionID) {
					[ 'upstreams', 'ipv4_nftsets', 'ipv6_nftsets', 'add_upstream_ips' ]
						.forEach(function(key) {
							uci.unset('updns', sectionID, key);
						});
				}

				moveFallbackRuleToLast();
				refreshFallbackRuleControls(m.root);
				return result;
			});
		};
		var handleRuleRemove = s.handleRemove;
		s.handleRemove = function() {
			return Promise.resolve(handleRuleRemove.apply(this, arguments)).then(function(result) {
				moveFallbackRuleToLast();
				refreshFallbackRuleControls(m.root);
				return result;
			});
		};

		var renderContents = m.renderContents;
		m.renderContents = function() {
			var activeTab = this.root && this.root.querySelector('.cbi-tabmenu > li.cbi-tab');
			var activeTabName = activeTab && activeTab.getAttribute('data-tab');

			dynamicOptionRefreshers.forEach(function(refresh) {
				refresh();
			});

			return renderContents.apply(this, arguments).then(function(node) {
				arrangeTabbedMap(node, data, localResolvers);
				installDomainListToggle();
				updateFallbackDragHandles(node);
				updateRuntimeStatus(node);

				if (!statusPollRegistered) {
					poll.add(function() {
						return updateRuntimeStatus(document.querySelector('.cbi-map'));
					}, 5);
					statusPollRegistered = true;
				}

				if (activeTabName) {
					var tabs = node.querySelectorAll('.cbi-tabmenu > li[data-tab]');
					Array.from(tabs).some(function(tab) {
						if (tab.getAttribute('data-tab') !== activeTabName)
							return false;

						var link = tab.querySelector('a');
						if (link)
							link.click();
						return true;
					});
				}

				return node;
			});
		};
		var cloneRuleOptions = s.cloneOptions;
		s.cloneOptions = function(source, destination) {
			if (source === this) {
				dynamicOptionRefreshers.forEach(function(refresh) {
					refresh();
				});
			}
			return cloneRuleOptions.call(this, source, destination);
		};
		return m.render();
	}
});
