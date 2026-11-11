// SPDX-License-Identifier: Apache-2.0

// Translates docs/ into the content of the site, so that docs/ stays the only
// source and still reads on GitHub. For each Markdown file of docs/:
//
//   - its first heading becomes the title of the page (Starlight needs a
//     frontmatter title, docs/ has none);
//   - links to other pages of docs/ become links to the site;
//   - links to the rest of the repository (code, LICENSE...) point to GitHub;
//   - a banner names the latest release, since the site follows main.
//
// It also copies docs/images, the logo and favicon of the site, and the
// landing page, which lives in site/src/landing: it is written for the site,
// not for GitHub.

import { execFileSync } from 'node:child_process';
import { cp, mkdir, readdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { dirname, join, posix, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const BASE = '/vesper';
const GITHUB = 'https://github.com/eventide-oss/vesper';

const site = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repo = resolve(site, '..');
const docs = join(repo, 'docs');
const content = join(site, 'src/content/docs');

// The learning journal holds only its template for now: it stays on GitHub.
const excluded = (rel) => rel.startsWith('journal/');

async function markdownFiles(dir, prefix = '') {
	const files = [];
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const rel = prefix + entry.name;
		if (entry.isDirectory()) files.push(...(await markdownFiles(join(dir, entry.name), `${rel}/`)));
		else if (entry.name.endsWith('.md') && !excluded(rel)) files.push(rel);
	}
	return files;
}

// docs/README.md, the index of the docs, is the Overview page: the home page
// of the site is the landing page. Other files keep their path.
const outputPath = (rel) => (rel === 'README.md' ? 'overview.md' : rel.replace(/(^|\/)README\.md$/, '$1index.md'));

// The URL Starlight gives a page: each segment of its path slugged as GitHub
// does for headings (lowercase, punctuation such as "." dropped).
function route(rel) {
	const slug = outputPath(rel)
		.replace(/\.md$/, '')
		.replace(/(^|\/)index$/, '')
		.split('/')
		.map((segment) => segment.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-'))
		.join('/');
	return slug ? `${BASE}/${slug}/` : `${BASE}/`;
}

async function isDirectory(path) {
	try {
		return (await stat(path)).isDirectory();
	} catch {
		return false;
	}
}

async function rewriteLink(target, rel, pages) {
	if (/^([a-z][a-z0-9+.-]*:|#|\/)/i.test(target)) return target;
	const [path, ...hash] = target.split('#');
	const anchor = hash.length ? `#${hash.join('#')}` : '';
	const resolved = posix.normalize(posix.join('docs', posix.dirname(rel), path)).replace(/\/$/, '');
	if (resolved.startsWith('docs/')) {
		const inDocs = resolved.slice('docs/'.length);
		if (pages.has(inDocs)) return route(inDocs) + anchor;
		if (pages.has(`${inDocs}/README.md`)) return route(`${inDocs}/README.md`) + anchor;
		if (inDocs.startsWith('images/')) return `${BASE}/${inDocs}`;
	}
	const kind = (await isDirectory(join(repo, resolved))) ? 'tree' : 'blob';
	return `${GITHUB}/${kind}/main/${resolved}${anchor}`;
}

async function translate(source, rel, pages, banner) {
	const lines = source.split('\n');
	const first = lines.findIndex((line) => line.trim() !== '');
	const heading = lines[first]?.match(/^#\s+(.+)$/);
	if (!heading) throw new Error(`docs/${rel}: the first line must be a "# Title" heading`);
	lines.splice(first, 1);

	let fenced = false;
	for (let i = 0; i < lines.length; i++) {
		if (/^\s*(```|~~~)/.test(lines[i])) fenced = !fenced;
		if (fenced) continue;
		const links = [...lines[i].matchAll(/\]\(([^)\s]+)\)/g)];
		for (const [whole, target] of links.reverse()) {
			lines[i] = lines[i].replace(whole, `](${await rewriteLink(target, rel, pages)})`);
		}
	}

	const frontmatter = ['---', `title: ${JSON.stringify(heading[1].replaceAll('`', ''))}`];
	if (banner) frontmatter.push('banner:', `  content: ${JSON.stringify(banner)}`);
	frontmatter.push('---', '');
	return frontmatter.join('\n') + lines.join('\n');
}

// The newest tag without a suffix: the latest release, not a candidate.
function latestRelease() {
	try {
		const tags = execFileSync('git', ['tag', '--list', 'v*', '--sort=-v:refname'], { cwd: repo, encoding: 'utf8' });
		return tags.split('\n').find((tag) => /^v\d+\.\d+\.\d+$/.test(tag));
	} catch {
		return undefined;
	}
}

const release = latestRelease();
const banner = release
	? `These docs follow the main branch. The latest release is <a href="${GITHUB}/releases/tag/${release}">${release}</a>.`
	: undefined;

const files = await markdownFiles(docs);
const pages = new Set(files);
await rm(content, { recursive: true, force: true });
for (const rel of files) {
	const out = join(content, outputPath(rel));
	await mkdir(dirname(out), { recursive: true });
	await writeFile(out, await translate(await readFile(join(docs, rel), 'utf8'), rel, pages, banner));
}

await rm(join(site, 'public/images'), { recursive: true, force: true });
await cp(join(docs, 'images'), join(site, 'public/images'), { recursive: true });
await mkdir(join(site, 'src/assets'), { recursive: true });
for (const logo of ['icon.svg', 'mark.svg', 'mark-night.svg']) await cp(join(docs, 'images', logo), join(site, 'src/assets', logo));
await cp(join(docs, 'images/icon.svg'), join(site, 'public/favicon.svg'));
await cp(join(site, 'src/landing/index.mdx'), join(content, 'index.mdx'));

console.log(`synced ${files.length} pages from docs/${release ? `, latest release ${release}` : ''}`);
