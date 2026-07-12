import assert from 'node:assert/strict';
import test from 'node:test';

import {
  createPublicGatewayHandler,
  createRaildrop,
  createRouteHandler,
  RaildropError,
  syncGlobalAssets,
} from '../src/server';
import { createSessionToken, readSessionToken } from '../src/server/session';
import { FakeRaildropStorage } from '../src/testing';

const secret = 'test-secret-that-is-at-least-thirty-two-characters';

test('signed sessions reject expiry and the wrong secret', () => {
  const sessionToken = createSessionToken(
    {
      version: 1,
      id: 'upload-id',
      endpoint: 'file',
      key: 'public/files/upload-id/file.txt',
      file: { name: 'file.txt', size: 4, type: 'text/plain' },
      access: 'public',
      retention: 'permanent',
      expiresAt: null,
      uploadExpiresAt: Date.now() - 1,
      metadata: { organizationId: 'org-1' },
      input: null,
      multipart: null,
    },
    secret
  );
  assert.throws(
    () => readSessionToken(sessionToken, secret),
    (error) => error instanceof RaildropError && error.code === 'EXPIRED'
  );
  assert.throws(
    () => readSessionToken(sessionToken, 'another-secret-that-is-at-least-thirty-two-characters'),
    (error) => error instanceof RaildropError && error.code === 'FORBIDDEN'
  );
});

test('prepare defaults public and finalize verifies the stored object', async () => {
  const storage = new FakeRaildropStorage();
  let callbackId: string | null = null;
  const f = createRaildrop();
  const router = {
    image: f({ image: { maxFileSize: '4MB', maxFileCount: 1 } })
      .middleware(() => ({ organizationId: 'org-1' }))
      .namespace(({ metadata }) => ['organizations', metadata.organizationId, 'images'])
      .onUploadComplete(({ file }) => {
        callbackId = file.id;
        return { ok: true as const };
      }),
  };
  const handler = createRouteHandler({
    router,
    storage,
    secret,
    publicBaseUrl: 'https://assets.test',
  });
  const prepare = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'prepare',
        endpoint: 'image',
        files: [{ name: 'photo.png', type: 'image/png', size: 3 }],
      }),
    })
  );
  assert.equal(prepare.status, 200);
  const [prepared] = (await prepare.json()) as {
    id: string;
    key: string;
    sessionToken: string;
    upload: { kind: 'put'; headers: Record<string, string> };
  }[];
  assert.ok(prepared);
  assert.match(prepared.key, /^public\/organizations\/org-1\/images\//);
  assert.deepEqual(prepared.upload.headers, {
    'Content-Type': 'image/png',
    'x-amz-meta-raildrop-access': 'public',
    'x-amz-meta-raildrop-id': prepared.id,
    'x-amz-meta-raildrop-name': 'cGhvdG8ucG5n',
    'x-amz-meta-raildrop-retention': 'permanent',
  });
  assert.equal(
    prepared.sessionToken
      .split('.')
      .slice(1, 4)
      .map((part) => Buffer.from(part, 'base64url').toString('utf8'))
      .some((part) => part.includes('org-1')),
    false
  );
  const wrongEndpoint = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'finalize',
        endpoint: 'another-route',
        sessionToken: prepared.sessionToken,
      }),
    })
  );
  assert.equal(wrongEndpoint.status, 403);
  await storage.put({
    key: prepared.key,
    body: 'ab',
    contentType: 'image/png',
    metadata: {
      'raildrop-id': prepared.id,
      'raildrop-access': 'public',
      'raildrop-retention': 'permanent',
    },
  });
  const mismatched = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'finalize',
        endpoint: 'image',
        sessionToken: prepared.sessionToken,
      }),
    })
  );
  assert.equal(mismatched.status, 400);
  await storage.put({
    key: prepared.key,
    body: 'abc',
    contentType: 'image/png',
    metadata: {
      'raildrop-id': prepared.id,
      'raildrop-access': 'public',
      'raildrop-retention': 'permanent',
    },
  });
  const finalize = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'finalize',
        endpoint: 'image',
        sessionToken: prepared.sessionToken,
      }),
    })
  );
  const result = (await finalize.json()) as { id: string; publicUrl: string; serverData: unknown };
  assert.equal(finalize.status, 200);
  assert.equal(result.publicUrl, `https://assets.test/${prepared.key}`);
  assert.equal(callbackId, prepared.id);

  const tampered = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'finalize',
        endpoint: 'image',
        sessionToken: `${prepared.sessionToken}x`,
      }),
    })
  );
  assert.equal(tampered.status, 403);
});

test('prepare enforces MIME type, size, and per-type file count before signing', async () => {
  const storage = new FakeRaildropStorage();
  const f = createRaildrop();
  const handler = createRouteHandler({
    router: {
      image: f({ image: { maxFileSize: '1KB', maxFileCount: 1 } }),
    },
    storage,
    secret,
    publicBaseUrl: 'https://assets.test',
  });
  const prepare = (files: { name: string; type: string; size: number }[]) =>
    handler(
      new Request('https://api.test/raildrop', {
        method: 'POST',
        body: JSON.stringify({ action: 'prepare', endpoint: 'image', files }),
      })
    );

  const invalidType = await prepare([{ name: 'payload.pdf', type: 'application/pdf', size: 10 }]);
  assert.equal(invalidType.status, 400);
  assert.equal(
    ((await invalidType.json()) as { error: { code: string } }).error.code,
    'INVALID_TYPE'
  );

  const tooLarge = await prepare([{ name: 'photo.png', type: 'image/png', size: 1_001 }]);
  assert.equal(tooLarge.status, 413);
  assert.equal(((await tooLarge.json()) as { error: { code: string } }).error.code, 'TOO_LARGE');

  const tooMany = await prepare([
    { name: 'first.png', type: 'image/png', size: 10 },
    { name: 'second.png', type: 'image/png', size: 10 },
  ]);
  assert.equal(tooMany.status, 400);
  assert.equal(((await tooMany.json()) as { error: { code: string } }).error.code, 'BAD_REQUEST');
  assert.equal(storage.objects.size, 0);
});

test('private temporary multipart preparation exposes exact byte ranges', async () => {
  const storage = new FakeRaildropStorage();
  const f = createRaildrop();
  const handler = createRouteHandler({
    router: {
      document: f({ pdf: { maxFileSize: '32MB' } })
        .access('private')
        .retention('temporary')
        .namespace(() => ['documents']),
    },
    storage,
    secret,
    publicBaseUrl: 'https://assets.test',
    multipartThresholdBytes: 5,
    multipartPartSizeBytes: 5_000_000,
  });
  const size = 11_000_001;
  const response = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'prepare',
        endpoint: 'document',
        files: [{ name: 'id.pdf', type: 'application/pdf', size }],
      }),
    })
  );
  const [prepared] = (await response.json()) as {
    key: string;
    publicUrl: null;
    sessionToken: string;
    upload: { kind: string; parts: { start: number; end: number }[] };
  }[];
  assert.ok(prepared);
  assert.match(prepared.key, /^private\/tmp\/\d+\/documents\//);
  assert.equal(prepared.upload.kind, 'multipart');
  assert.deepEqual(
    prepared.upload.parts.map((part) => [part.start, part.end]),
    [
      [0, 5_000_000],
      [5_000_000, 10_000_000],
      [10_000_000, size],
    ]
  );
  assert.equal(storage.multipart.size, 1);
  const aborted = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'abort',
        endpoint: 'document',
        sessionToken: prepared.sessionToken,
      }),
    })
  );
  assert.equal(aborted.status, 200);
  assert.equal(storage.multipart.size, 0);
});

test('public gateway hides private keys and resolves short-lived global aliases', async () => {
  const storage = new FakeRaildropStorage();
  await storage.put({
    key: 'private/users/u1/file.pdf',
    body: 'private',
    contentType: 'application/pdf',
    metadata: { 'raildrop-access': 'private' },
  });
  await syncGlobalAssets(storage, [
    {
      alias: 'product-template',
      body: new TextEncoder().encode('template'),
      filename: 'template.xlsx',
      contentType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      namespace: ['templates'],
    },
  ]);
  const gateway = createPublicGatewayHandler({ storage });
  assert.equal(
    (await gateway(new Request('https://assets.test/private/users/u1/file.pdf'))).status,
    404
  );
  const alias = await gateway(new Request('https://assets.test/global/product-template'));
  assert.equal(alias.status, 200);
  assert.equal(
    alias.headers.get('cache-control'),
    'public, max-age=300, stale-while-revalidate=86400'
  );
  assert.equal(await alias.text(), 'template');
});

test('malformed requests fail as bad requests without leaking server errors', async () => {
  const f = createRaildrop();
  const handler = createRouteHandler({
    router: { file: f({ blob: { maxFileSize: '1MB' } }) },
    storage: new FakeRaildropStorage(),
    secret,
    publicBaseUrl: 'https://assets.test',
  });
  const response = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({ action: 'prepare', endpoint: 'file', files: [null] }),
    })
  );
  assert.equal(response.status, 400);
  assert.equal(((await response.json()) as { error: { code: string } }).error.code, 'BAD_REQUEST');
  const invalidJson = await handler(
    new Request('https://api.test/raildrop', { method: 'POST', body: '{' })
  );
  assert.equal(invalidJson.status, 400);
});

test('callback failure can be retried with the same stable upload id', async () => {
  const storage = new FakeRaildropStorage();
  const f = createRaildrop();
  let attempts = 0;
  const handler = createRouteHandler({
    router: {
      file: f({ text: { maxFileSize: '1MB' } }).onUploadComplete(({ file }) => {
        attempts += 1;
        if (attempts === 1) throw new Error('retry me');
        return { uploadId: file.id };
      }),
    },
    storage,
    secret,
    publicBaseUrl: 'https://assets.test',
  });
  const prepare = await handler(
    new Request('https://api.test/raildrop', {
      method: 'POST',
      body: JSON.stringify({
        action: 'prepare',
        endpoint: 'file',
        files: [{ name: 'note.txt', type: 'text/plain', size: 4 }],
      }),
    })
  );
  const [prepared] = (await prepare.json()) as {
    id: string;
    key: string;
    sessionToken: string;
  }[];
  assert.ok(prepared);
  await storage.put({
    key: prepared.key,
    body: 'note',
    contentType: 'text/plain',
    metadata: {
      'raildrop-id': prepared.id,
      'raildrop-access': 'public',
      'raildrop-retention': 'permanent',
    },
  });
  const finalize = () =>
    handler(
      new Request('https://api.test/raildrop', {
        method: 'POST',
        body: JSON.stringify({
          action: 'finalize',
          endpoint: 'file',
          sessionToken: prepared.sessionToken,
        }),
      })
    );
  assert.equal((await finalize()).status, 502);
  const retry = await finalize();
  assert.equal(retry.status, 200);
  const result = (await retry.json()) as { id: string; serverData: { uploadId: string } };
  assert.equal(result.serverData.uploadId, prepared.id);
  assert.equal(result.id, prepared.id);
});

test('gateway supports conditional HEAD and refuses encoded traversal', async () => {
  const storage = new FakeRaildropStorage();
  await storage.put({
    key: 'public/files/upload/file.txt',
    body: 'hello',
    contentType: 'text/plain',
    cacheControl: 'public, max-age=31536000, immutable',
    metadata: { 'raildrop-access': 'public' },
  });
  const gateway = createPublicGatewayHandler({ storage });
  const head = await gateway(
    new Request('https://assets.test/public/files/upload/file.txt', { method: 'HEAD' })
  );
  assert.equal(head.status, 200);
  assert.equal(head.headers.get('content-length'), '5');
  const conditional = await gateway(
    new Request('https://assets.test/public/files/upload/file.txt', {
      headers: { 'If-None-Match': head.headers.get('etag') ?? '' },
    })
  );
  assert.equal(conditional.status, 304);
  const etagPrecedence = await gateway(
    new Request('https://assets.test/public/files/upload/file.txt', {
      headers: {
        'If-None-Match': '"different-etag"',
        'If-Modified-Since': new Date(Date.now() + 60_000).toUTCString(),
      },
    })
  );
  assert.equal(etagPrecedence.status, 200);
  assert.equal(
    (await gateway(new Request('https://assets.test/public/%2e%2e/private/file'))).status,
    404
  );
  assert.equal(
    (await gateway(new Request('https://assets.test/public/files/%00/file.txt'))).status,
    400
  );
  assert.equal(
    (await gateway(new Request('https://assets.test/public/files/%252e%252e/file.txt'))).status,
    400
  );
});

test('gateway preserves encoded namespace segments when reading storage keys', async () => {
  const storage = new FakeRaildropStorage();
  await storage.put({
    key: 'public/hello%20world/upload/file.txt',
    body: 'encoded namespace',
    contentType: 'text/plain',
    metadata: { 'raildrop-access': 'public', 'raildrop-retention': 'permanent' },
  });
  const gateway = createPublicGatewayHandler({ storage });
  const response = await gateway(
    new Request('https://assets.test/public/hello%20world/upload/file.txt')
  );

  assert.equal(response.status, 200);
  assert.equal(await response.text(), 'encoded namespace');
});

test('gateway caps public temporary caching at the object expiry', async () => {
  const storage = new FakeRaildropStorage();
  const expiresAt = new Date(Date.now() + 10 * 60 * 1000).toISOString();
  await storage.put({
    key: 'public/tmp/99999/files/upload/file.txt',
    body: 'temporary',
    contentType: 'text/plain',
    cacheControl: 'public, max-age=31536000, immutable',
    metadata: {
      'raildrop-access': 'public',
      'raildrop-retention': 'temporary',
      'raildrop-expires-at': expiresAt,
    },
  });
  const gateway = createPublicGatewayHandler({ storage });
  const response = await gateway(
    new Request('https://assets.test/public/tmp/99999/files/upload/file.txt')
  );
  const cacheControl = response.headers.get('cache-control') ?? '';
  const maxAge = Number(/max-age=(\d+)/.exec(cacheControl)?.[1]);

  assert.equal(response.status, 200);
  assert.equal(cacheControl.includes('immutable'), false);
  assert.equal(cacheControl.includes('must-revalidate'), true);
  assert.equal(maxAge > 0 && maxAge <= 600, true);
});
