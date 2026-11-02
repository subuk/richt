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
for (const term of ['inventory', 'services', 'reload', 'systemd', 'secret', 'continuation']) {
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

// The service section must remain navigable and searchable after splitting the
// original guide, including links back to its nested Markdown source files.
const servicePages = [
  ['services/', 'services/_index.md'],
  ['services/lifecycle/', 'services/lifecycle.md'],
  ['services/reactions/', 'services/reactions.md'],
  ['services/binaries/', 'services/binaries/_index.md'],
  ['services/binaries/rpm/', 'services/binaries/rpm.md'],
  ['services/configs/', 'services/configs/_index.md'],
  ['services/configs/files/', 'services/configs/files.md'],
  ['services/processes/', 'services/processes/_index.md'],
  ['services/processes/systemd/', 'services/processes/systemd.md'],
];
for (const [route, source] of servicePages) {
  const page = fs.readFileSync(path.join(output, 'docs', route, 'index.html'), 'utf8');
  assert(index.some(entry => entry.ref.endsWith(`/docs/${route}`)), `Service page missing from search: ${route}`);
  assert(page.includes(`/blob/main/docs/${source}`), `Incorrect nested source link: ${route}`);
}
const services = fs.readFileSync(path.join(output, 'docs/services/index.html'), 'utf8');
for (const fragment of ['package-requests', 'process-lifecycle', 'owned-units-and-drop-ins', 'removal-and-continuation', 'ready-package-walkthrough', 'developer-fixture-regression-suite']) {
  assert(services.includes(`id="${fragment}"`), `Original service guide fragment lost: ${fragment}`);
}
console.log(`Checked local search (${index.length} pages), source walkthroughs, and ${htmlFiles.length} HTML pages`);
