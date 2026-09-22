import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import test from 'node:test';

import { createSessionToken, readSessionToken } from '../src/server/session';

const fixturePath = resolve(import.meta.dirname, 'fixtures/go-session-conformance.json');
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  secret: string;
  metadataRaw: string;
  inputRaw: string;
  tsToken: string;
  goToken: string;
};

type ConformanceMetadata = {
  userId: string;
  role: string;
  '2': string;
  '10': string;
  note: string;
};

test('TypeScript reads tokens minted by the Go SDK', () => {
  const payload = readSessionToken(fixture.goToken, fixture.secret);
  assert.equal(payload.version, 1);
  assert.equal(payload.id, 'conformance-upload-id');
  assert.equal(payload.endpoint, 'avatar');
  assert.equal(payload.key, 'public/users/u_123/avatars/conformance-upload-id/file.png');
  assert.deepEqual(payload.file, {
    name: 'photo.png',
    size: 1234,
    type: 'image/png',
    lastModified: 1700000000000,
  });
  assert.equal(payload.access, 'public');
  assert.equal(payload.retention, 'permanent');
  assert.equal(payload.expiresAt, null);
  assert.equal(payload.uploadExpiresAt, 4102444800000);
  const metadata = payload.metadata as ConformanceMetadata;
  assert.equal(metadata.userId, 'u_123');
  assert.equal(metadata.role, 'admin');
  assert.equal(metadata['2'], 'two');
  assert.equal(metadata['10'], 'ten');
  assert.equal(metadata.note, 'line\u2028sep\u2029end');
  assert.deepEqual(payload.input, { albumId: 7 });
  assert.equal(payload.multipart, null);
  assert.equal(JSON.stringify(payload.metadata), fixture.metadataRaw);
  assert.equal(JSON.stringify(payload.input), fixture.inputRaw);
});

test('TypeScript-minted tokens stay stable across the fixture', () => {
  const payload = readSessionToken(fixture.tsToken, fixture.secret);
  assert.equal(payload.id, 'conformance-upload-id');
  assert.equal((payload.metadata as ConformanceMetadata).userId, 'u_123');
  assert.equal(JSON.stringify(payload.metadata), fixture.metadataRaw);
  assert.throws(
    () => readSessionToken(fixture.tsToken, 'another-secret-that-is-at-least-thirty-two-characters'),
    (error) => error instanceof Error && 'code' in error && (error as { code: string }).code === 'FORBIDDEN'
  );
});

test('TypeScript session format matches the Go SDK digest contract', () => {
  const token = createSessionToken(
    {
      version: 1,
      id: 'digest-contract',
      endpoint: 'avatar',
      key: 'public/users/u_123/avatars/digest-contract/file.png',
      file: { name: 'photo.png', size: 1234, type: 'image/png', lastModified: 1700000000000 },
      access: 'public',
      retention: 'permanent',
      expiresAt: null,
      uploadExpiresAt: 4102444800000,
      metadata: { userId: 'u_123', role: 'admin' },
      input: { albumId: 7 },
      multipart: null,
    },
    fixture.secret
  );
  const payload = readSessionToken(token, fixture.secret);
  assert.deepEqual(payload.metadata, { userId: 'u_123', role: 'admin' });
});
