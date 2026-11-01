import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const output = process.argv[2] ? path.resolve(process.argv[2]) : path.join(root, 'public');
const require = createRequire(import.meta.url);
const lunr = require('lunr');
const indexFiles = fs.readdirSync(output).filter(file => /^offline-search-index(?:\.[a-f0-9]+)?\.json$/.test(file));
assert.equal(indexFiles.length, 1, 'Expected one local search index');
const index = JSON.parse(fs.readFileSync(path.join(output, indexFiles[0]), 'utf8'));
assert(index.length >= 12, 'Documentation search index is incomplete');
const search = lunr(function () {
  this.ref('ref');
  this.field('title');
  this.field('body');
  index.forEach(page => this.add(page));
});
for (const term of ['inventory', 'systemd', 'secret', 'continuation']) {
  assert(search.search(term).length, `Local search has no results for ${term}`);
}
assert(index.some(page => JSON.stringify(page).includes('/docs/walkthroughs/vmango/')), 'Source-rendered walkthrough missing from search');
assert(fs.existsSync(path.join(output, 'vendor/lunr.min.js')), 'Local Lunr asset missing');
const htmlFiles = fs.readdirSync(output, { recursive: true }).filter(file => file.endsWith('.html'));
for (const file of htmlFiles) {
  const html = fs.readFileSync(path.join(output, file), 'utf8');
  assert(!/<script[^>]+src=["']https?:\/\//i.test(html), `Third-party script in ${file}`);
  assert(!/\/Users\/|\/home\/|USERNAME\/REPOSITORY|cse\.google|googletagmanager|google-analytics/.test(html), `Placeholder, machine path, or remote search/tracking in ${file}`);
  if (file !== '404.html') {
    assert(html.includes('id="theme-mode"'), `Theme control missing in ${file}`);
    assert(html.includes('/landing/theme.js'), `Shared theme script missing in ${file}`);
    assert(html.includes('/landing/copy.js'), `Copy control missing in ${file}`);
  }
}
const walkthrough = fs.readFileSync(path.join(output, 'docs/walkthroughs/vmango/index.html'), 'utf8');
assert(walkthrough.includes('href="#exact-upgrade-downgrade-and-config-reaction"'), 'Walkthrough table of contents missing');
assert(!walkthrough.includes('<h1 id="vmango-rpm-and-systemd-walkthrough"'), 'Source README title duplicated');
console.log(`Checked local search (${index.length} pages), source walkthroughs, and ${htmlFiles.length} HTML pages`);
