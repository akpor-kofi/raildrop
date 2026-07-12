import assert from 'node:assert/strict';
import test from 'node:test';

import { genUploader } from '../src/client';
import { createRaildrop } from '../src/server';

import type { RaildropPreparedFile, RaildropUploadedFile } from '../src';
import type { RaildropResumeState, RaildropResumeStore } from '../src/client';

const f = createRaildrop();
const _router = {
  file: f({ blob: { maxFileSize: '4MB' } }).onUploadComplete(() => ({ ok: true as const })),
};

const clientFile = (body: string) =>
  Object.assign(new Blob([body], { type: 'application/octet-stream' }), {
    name: 'file.bin',
    lastModified: 1,
  });

const prepared = (): RaildropPreparedFile => ({
  id: 'upload-id',
  key: 'public/uploads/upload-id/file.bin',
  name: 'file.bin',
  type: 'application/octet-stream',
  size: 6,
  access: 'public',
  retention: 'permanent',
  expiresAt: null,
  upload: {
    kind: 'multipart',
    uploadId: 'multipart-id',
    parts: [
      { partNumber: 1, start: 0, end: 3, url: 'https://storage.test/part-1' },
      { partNumber: 2, start: 3, end: 6, url: 'https://storage.test/part-2' },
    ],
  },
  sessionToken: 'session-token',
});

test('client retries failed multipart parts and finalizes in part order', async () => {
  const attempts = new Map<string, number>();
  let finalizedParts: unknown;
  let finalizeAttempts = 0;
  const fetcher: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url === 'https://api.test/raildrop') {
      const body = JSON.parse(String(init?.body)) as { action: string; parts?: unknown };
      if (body.action === 'prepare') return Response.json([prepared()]);
      finalizeAttempts += 1;
      finalizedParts = body.parts;
      if (finalizeAttempts === 1) {
        return Response.json(
          { error: { code: 'STORAGE_ERROR', message: 'Finalize response was lost.' } },
          { status: 503 }
        );
      }
      const result: RaildropUploadedFile<{ ok: true }> = {
        id: 'upload-id',
        key: 'public/uploads/upload-id/file.bin',
        access: 'public',
        retention: 'permanent',
        publicUrl: 'https://assets.test/public/uploads/upload-id/file.bin',
        name: 'file.bin',
        type: 'application/octet-stream',
        size: 6,
        etag: 'complete',
        expiresAt: null,
        serverData: { ok: true },
      };
      return Response.json(result);
    }
    const count = (attempts.get(url) ?? 0) + 1;
    attempts.set(url, count);
    if (url.endsWith('part-2') && count === 1) return new Response(null, { status: 503 });
    return new Response(null, { status: 200, headers: { etag: `etag-${url.at(-1)}` } });
  };
  const uploader = genUploader<typeof _router>({
    url: 'https://api.test/raildrop',
    fetch: fetcher,
    retryAttempts: 1,
    partConcurrency: 2,
  });
  const result = await uploader.uploadFiles('file', {
    files: [clientFile('abcdef')],
    input: undefined,
  });
  assert.equal(result[0]?.serverData.ok, true);
  assert.equal(attempts.get('https://storage.test/part-2'), 2);
  assert.equal(finalizeAttempts, 2);
  assert.deepEqual(finalizedParts, [
    { partNumber: 1, etag: 'etag-1' },
    { partNumber: 2, etag: 'etag-2' },
  ]);
});

test('client resumes persisted multipart parts without preparing again', async () => {
  const persisted: RaildropResumeState = {
    prepared: prepared(),
    completedParts: [{ partNumber: 1, etag: 'etag-1' }],
    savedAt: Date.now(),
  };
  let removed = false;
  const store: RaildropResumeStore = {
    get: async () => persisted,
    set: async () => undefined,
    remove: async () => void (removed = true),
  };
  let preparedAgain = false;
  let partOneUploaded = false;
  const fetcher: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url.endsWith('part-1')) partOneUploaded = true;
    if (url.endsWith('part-2')) return new Response(null, { headers: { etag: 'etag-2' } });
    const body = JSON.parse(String(init?.body)) as { action: string };
    if (body.action === 'prepare') preparedAgain = true;
    return Response.json({
      id: 'upload-id',
      key: 'public/uploads/upload-id/file.bin',
      access: 'public',
      retention: 'permanent',
      publicUrl: 'https://assets.test/file.bin',
      name: 'file.bin',
      type: 'application/octet-stream',
      size: 6,
      etag: 'complete',
      expiresAt: null,
      serverData: { ok: true },
    });
  };
  await genUploader<typeof _router>({
    url: 'https://api.test/raildrop',
    fetch: fetcher,
    resumeStore: store,
  }).uploadFiles('file', { files: [clientFile('abcdef')], input: undefined });
  assert.equal(preparedAgain, false);
  assert.equal(partOneUploaded, false);
  assert.equal(removed, true);
});

test('client does not reuse multipart state for different file bytes', async () => {
  const state = new Map<string, RaildropResumeState>();
  const store: RaildropResumeStore = {
    get: async (key) => state.get(key) ?? null,
    set: async (key, value) => void state.set(key, value),
    remove: async (key) => void state.delete(key),
  };
  let prepareCalls = 0;
  let failPartTwo = true;
  const fetcher: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url === 'https://api.test/raildrop') {
      const body = JSON.parse(String(init?.body)) as { action: string };
      if (body.action === 'prepare') {
        prepareCalls += 1;
        return Response.json([prepared()]);
      }
      return Response.json({
        id: 'upload-id',
        key: 'public/uploads/upload-id/file.bin',
        access: 'public',
        retention: 'permanent',
        publicUrl: 'https://assets.test/file.bin',
        name: 'file.bin',
        type: 'application/octet-stream',
        size: 6,
        etag: 'complete',
        expiresAt: null,
        serverData: { ok: true },
      });
    }
    if (url.endsWith('part-2') && failPartTwo) return new Response(null, { status: 503 });
    return new Response(null, { status: 200, headers: { etag: `etag-${url.at(-1)}` } });
  };
  const uploader = genUploader<typeof _router>({
    url: 'https://api.test/raildrop',
    fetch: fetcher,
    retryAttempts: 0,
    partConcurrency: 1,
    resumeStore: store,
    resumeScope: 'user-1',
  });

  await assert.rejects(
    uploader.uploadFiles('file', { files: [clientFile('abcdef')], input: undefined })
  );
  failPartTwo = false;
  await uploader.uploadFiles('file', { files: [clientFile('ghijkl')], input: undefined });

  assert.equal(prepareCalls, 2);
});

test('client forwards server-required headers for presigned PUT uploads', async () => {
  const captured: { headers: Headers | null } = { headers: null };
  const fetcher: typeof fetch = async (input, init) => {
    if (String(input) === 'https://api.test/raildrop') {
      const body = JSON.parse(String(init?.body)) as { action: string };
      if (body.action === 'prepare') {
        return Response.json([
          {
            id: 'upload-id',
            key: 'public/uploads/upload-id/file.bin',
            name: 'file.bin',
            type: 'application/octet-stream',
            size: 6,
            access: 'public',
            retention: 'permanent',
            expiresAt: null,
            upload: {
              kind: 'put',
              url: 'https://storage.test/object',
              headers: {
                'Content-Type': 'application/octet-stream',
                'x-amz-meta-raildrop-id': 'upload-id',
                'x-amz-meta-raildrop-access': 'public',
                'x-amz-meta-raildrop-retention': 'permanent',
              },
            },
            sessionToken: 'session-token',
          },
        ]);
      }
      return Response.json({
        id: 'upload-id',
        key: 'public/uploads/upload-id/file.bin',
        access: 'public',
        retention: 'permanent',
        publicUrl: 'https://assets.test/file.bin',
        name: 'file.bin',
        type: 'application/octet-stream',
        size: 6,
        etag: 'complete',
        expiresAt: null,
        serverData: { ok: true },
      });
    }
    captured.headers = new Headers(init?.headers);
    return new Response(null, { status: 200 });
  };

  await genUploader<typeof _router>({
    url: 'https://api.test/raildrop',
    fetch: fetcher,
  }).uploadFiles('file', { files: [clientFile('abcdef')], input: undefined });

  assert.ok(captured.headers);
  assert.equal(captured.headers.get('content-type'), 'application/octet-stream');
  assert.equal(captured.headers.get('x-amz-meta-raildrop-id'), 'upload-id');
  assert.equal(captured.headers.get('x-amz-meta-raildrop-access'), 'public');
  assert.equal(captured.headers.get('x-amz-meta-raildrop-retention'), 'permanent');
});

test('client prepares multiple files as one policy-enforced batch', async () => {
  const first = Object.assign(clientFile('first'), { name: 'first.bin' });
  const second = Object.assign(clientFile('second'), { name: 'second.bin' });
  let prepareCalls = 0;
  let preparedFileCount = 0;
  const fetcher: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url === 'https://api.test/raildrop') {
      const body = JSON.parse(String(init?.body)) as {
        action: string;
        files?: { name: string; size: number; type: string }[];
        sessionToken?: string;
      };
      if (body.action === 'prepare') {
        prepareCalls += 1;
        preparedFileCount = body.files?.length ?? 0;
        return Response.json(
          (body.files ?? []).map((file, index) => ({
            id: `upload-${index}`,
            key: `public/uploads/upload-${index}/file.bin`,
            name: file.name,
            type: file.type,
            size: file.size,
            access: 'public',
            retention: 'permanent',
            expiresAt: null,
            upload: {
              kind: 'put',
              url: `https://storage.test/object-${index}`,
              headers: { 'Content-Type': file.type },
            },
            sessionToken: `session-${index}`,
          }))
        );
      }
      const index = body.sessionToken === 'session-1' ? 1 : 0;
      return Response.json({
        id: `upload-${index}`,
        key: `public/uploads/upload-${index}/file.bin`,
        access: 'public',
        retention: 'permanent',
        publicUrl: `https://assets.test/file-${index}.bin`,
        name: index === 0 ? first.name : second.name,
        type: 'application/octet-stream',
        size: index === 0 ? first.size : second.size,
        etag: `etag-${index}`,
        expiresAt: null,
        serverData: { ok: true },
      });
    }
    return new Response(null, { status: 200 });
  };

  const result = await genUploader<typeof _router>({
    url: 'https://api.test/raildrop',
    fetch: fetcher,
  }).uploadFiles('file', { files: [first, second], input: undefined });

  assert.equal(prepareCalls, 1);
  assert.equal(preparedFileCount, 2);
  assert.equal(result.length, 2);
});
