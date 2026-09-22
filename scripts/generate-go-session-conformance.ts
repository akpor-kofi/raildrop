import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { createSessionToken } from '../src/server/session';

const rootDirectory = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const fixtureDirectory = resolve(rootDirectory, 'test/fixtures');
const fixturePath = resolve(fixtureDirectory, 'go-session-conformance.json');

const secret = 'conformance-secret-0123456789abcdef0123456789';

const payload = {
  version: 1,
  id: 'conformance-upload-id',
  endpoint: 'avatar',
  key: 'public/users/u_123/avatars/conformance-upload-id/file.png',
  file: { name: 'photo.png', size: 1234, type: 'image/png', lastModified: 1700000000000 },
  access: 'public',
  retention: 'permanent',
  expiresAt: null,
  uploadExpiresAt: 4102444800000,
  metadata: { userId: 'u_123', role: 'admin' },
  input: { albumId: 7 },
  multipart: null,
} as const;

const metadataRaw = JSON.stringify({ userId: 'u_123', role: 'admin' });
const inputRaw = JSON.stringify({ albumId: 7 });

mkdirSync(fixtureDirectory, { recursive: true });
const bootstrap = {
  secret,
  metadataRaw,
  inputRaw,
  tsToken: '',
  goToken: '',
};
writeFileSync(fixturePath, `${JSON.stringify(bootstrap, null, 2)}\n`);

const output = execFileSync('go', ['test', './', '-run', 'TestMintConformanceToken', '-v'], {
  cwd: resolve(rootDirectory, 'sdk/go'),
  env: {
    ...process.env,
    CGO_ENABLED: '0',
    GOTOOLCHAIN: 'local',
    RAILDROP_MINT_CONFORMANCE_TOKEN: '1',
    RAILDROP_CONFORMANCE_FIXTURE: fixturePath,
  },
  encoding: 'utf8',
});

const lines = output
  .split('\n')
  .map((line) => line.trim())
  .filter((line) => /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(line));

if (lines.length !== 1) {
  console.error(output);
  throw new Error('Expected exactly one minted token from the Go conformance hook.');
}
const goToken = lines[0];

const tsToken = createSessionToken(
  {
    ...payload,
    metadata: JSON.parse(metadataRaw),
    input: JSON.parse(inputRaw),
  },
  secret
);

mkdirSync(dirname(fixturePath), { recursive: true });
writeFileSync(
  fixturePath,
  `${JSON.stringify(
    {
      secret,
      metadataRaw,
      inputRaw,
      tsToken,
      goToken,
    },
    null,
    2
  )}\n`
);

console.log(`Wrote ${fixturePath}`);
