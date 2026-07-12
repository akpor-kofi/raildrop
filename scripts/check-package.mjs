import assert from 'node:assert/strict';
import { access, readFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';

const packageRoot = new URL('../', import.meta.url);
const packageJson = JSON.parse(await readFile(new URL('package.json', packageRoot), 'utf8'));

const collectTargets = (value) => {
  if (typeof value === 'string') return [value];
  return Object.values(value).flatMap(collectTargets);
};

const exportTargets = collectTargets(packageJson.exports);
const requiredFiles = new Set([
  packageJson.bin.raildrop,
  packageJson.main,
  packageJson.module,
  packageJson.types,
  ...exportTargets,
]);

for (const target of requiredFiles) {
  await access(new URL(target.replace(/^\.\//, ''), packageRoot));
}

const launcher = await readFile(
  new URL(packageJson.bin.raildrop.replace(/^\.\//, ''), packageRoot),
  'utf8'
);
assert.match(launcher, /^#!\/usr\/bin\/env node\n/);

const packed = spawnSync('npm', ['pack', '--dry-run', '--json', '--ignore-scripts'], {
  cwd: packageRoot,
  encoding: 'utf8',
});
assert.equal(packed.status, 0, packed.stderr);

const [manifest] = JSON.parse(packed.stdout);
const packedFiles = new Set(manifest.files.map((file) => file.path));
for (const target of requiredFiles) {
  assert.ok(
    packedFiles.has(target.replace(/^\.\//, '')),
    `Packed Raildrop package is missing ${target}.`
  );
}
