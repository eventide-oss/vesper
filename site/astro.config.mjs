// SPDX-License-Identifier: Apache-2.0
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';
import mermaid from 'astro-mermaid';

// The pages come from docs/, translated by scripts/sync-docs.mjs.
export default defineConfig({
	site: 'https://eventide-oss.github.io',
	base: '/vesper',
	trailingSlash: 'always',
	integrations: [
		// Before Starlight, so that its code blocks are left to Mermaid.
		mermaid({ autoTheme: true }),
		starlight({
			title: 'Vesper',
			description: 'Ephemeral preview environments for Kubernetes, one per pull request.',
			logo: { light: './src/assets/mark-night.svg', dark: './src/assets/mark.svg' },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/eventide-oss/vesper' }],
			customCss: [
				'@fontsource-variable/inter',
				'@fontsource-variable/jetbrains-mono',
				'./src/styles/vesper.css',
			],
			sidebar: [
				'overview',
				{ label: 'Start', items: ['quickstart', 'try-on-a-server', 'cheatsheet'] },
				{ label: 'Reference', items: ['cli', 'crd-reference', 'metrics'] },
				{
					label: 'Understand',
					items: [
						'architecture',
						'security',
						{ label: 'Decision records', items: [{ autogenerate: { directory: 'adr' } }] },
					],
				},
				{ label: 'Project', items: ['development', 'release', 'acceptance-v01', 'brand'] },
			],
		}),
	],
});
