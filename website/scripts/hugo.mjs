import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = fileURLToPath(new URL('../', import.meta.url));
const command = process.argv[2];
if (!['build', 'serve'].includes(command)) throw new Error('Expected build or serve');
// Hugo speaks the embedded protocol directly; prefer the native Sass compiler
// over the JavaScript sass shim also installed by hugo-extended.
const sassPlatform = `${process.platform}-${process.arch}`;
const env = { ...process.env, PATH: [path.join(root, `node_modules/sass-embedded-${sassPlatform}/dart-sass`), path.join(root, 'node_modules/.bin'), process.env.PATH].join(path.delimiter) };
// Keep Hugo module downloads and generated resources inside this checkout.
env.HUGO_CACHEDIR = path.join(root, '.cache/hugo');
env.GOMODCACHE = path.join(root, '.cache/gomod');
const run = (args) => {
  const result = spawnSync(path.join(root, 'node_modules/.bin/hugo'), args, { cwd: root, env, stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
};
// Building first also creates the fingerprinted local-search index for preview.
run(['--cleanDestinationDir', '--environment', command === 'serve' ? 'development' : 'production', ...(command === 'build' ? process.argv.slice(3) : [])]);
if (command === 'serve') run(['server', '--bind', '127.0.0.1', '--port', '1313', '--renderToMemory', '--disableFastRender', ...process.argv.slice(3)]);
