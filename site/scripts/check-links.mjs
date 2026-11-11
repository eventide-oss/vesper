// SPDX-License-Identifier: Apache-2.0

// Fails the build if a page of the site links to a page or a file of the site
// that does not exist, or to a heading that is not on its page. Links to
// other sites are not checked: they would make the build depend on the
// network.

import { readdir, readFile } from 'node:fs/promises';
import { join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = '/vesper/';
const dist = resolve(fileURLToPath(import.meta.url), '../../dist');

async function files(dir) {
	const out = [];
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...(await files(path)));
		else out.push(path);
	}
	return out;
}

const all = await files(dist);
const known = new Set(all.map((path) => BASE + relative(dist, path).split(sep).join('/')));
const pages = all.filter((path) => path.endsWith('.html'));
const ids = new Map();
for (const page of pages) {
	const html = await readFile(page, 'utf8');
	ids.set(BASE + relative(dist, page).split(sep).join('/'), new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
}

const broken = [];
for (const page of pages) {
	const from = BASE + relative(dist, page).split(sep).join('/');
	const html = await readFile(page, 'utf8');
	for (const [, href] of html.matchAll(/\shref="([^"]+)"/g)) {
		if (!href.startsWith(BASE)) continue;
		const [path, anchor] = href.split('#');
		const target = path.endsWith('/') ? `${path}index.html` : path;
		if (!known.has(target)) broken.push(`${from}: ${href} (no such page or file)`);
		else if (anchor && ids.has(target) && !ids.get(target).has(decodeURIComponent(anchor)))
			broken.push(`${from}: ${href} (no such heading)`);
	}
}

if (broken.length) {
	console.error(`${broken.length} broken link(s):\n  ${[...new Set(broken)].join('\n  ')}`);
	process.exit(1);
}
console.log(`links checked: ${pages.length} pages, no broken link`);
