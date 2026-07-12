import assert from 'node:assert/strict';
import test from 'node:test';

import { RaildropError, encodeNamespace, namespace, parseFileSize } from '../src';
import { cleanupExpiredObjects, RaildropApi, syncGlobalAssets } from '../src/server';
import { FakeRaildropStorage } from '../src/testing';

test('namespace validates and encodes segments', () => {
  assert.equal(
    encodeNamespace(namespace('organizations', 'hello world')),
    'organizations/hello%20world'
  );
  assert.throws(() => namespace('..'), RaildropError);
  assert.throws(() => namespace('a/b'), RaildropError);
  assert.throws(() => namespace('%2e%2e'), RaildropError);
});

test('file size parser accepts readable limits', () => {
  assert.equal(parseFileSize('4MB'), 4_000_000);
  assert.equal(parseFileSize(1024), 1024);
  assert.throws(() => parseFileSize('wat' as '4MB'), RaildropError);
});

test('global asset sync is immutable and writes manifest last', async () => {
  const storage = new FakeRaildropStorage();
  const manifest = await syncGlobalAssets(storage, [
    {
      alias: 'product-template',
      body: new TextEncoder().encode('template'),
      filename: 'template.xlsx',
      contentType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      namespace: ['templates', 'products'],
    },
  ]);
  assert.match(
    manifest.assets['product-template']?.key ?? '',
    /^public\/global\/templates\/products\//
  );
  assert.ok(storage.objects.has('public/global/_raildrop/manifest.json'));
});

test('temporary cleanup only deletes expired temp prefixes', async () => {
  const storage = new FakeRaildropStorage();
  const today = Math.floor(Date.now() / 86_400_000);
  await storage.put({
    key: `private/tmp/${today - 1}/x/id/file.pdf`,
    body: 'old',
    contentType: 'application/pdf',
  });
  await storage.put({
    key: `private/tmp/${today + 1}/x/id/file.pdf`,
    body: 'new',
    contentType: 'application/pdf',
  });
  await storage.put({
    key: 'private/x/id/file.pdf',
    body: 'permanent',
    contentType: 'application/pdf',
  });
  const result = await cleanupExpiredObjects(storage);
  assert.equal(result.objectsDeleted, 1);
  assert.equal(storage.objects.size, 2);
});

test('server uploads reject permanent objects with an expiry', async () => {
  const api = new RaildropApi(new FakeRaildropStorage(), 'https://assets.test');
  await assert.rejects(
    api.uploadFiles({
      body: new TextEncoder().encode('file'),
      name: 'file.txt',
      type: 'text/plain',
      namespace: ['documents'],
      retention: 'permanent',
      expiresAt: new Date(Date.now() + 60_000),
    }),
    RaildropError
  );
});

test('global asset sync reports superseded immutable versions', async () => {
  const storage = new FakeRaildropStorage();
  const source = (body: string) => ({
    alias: 'product-template',
    body: new TextEncoder().encode(body),
    filename: 'template.xlsx',
    contentType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    namespace: ['templates'],
  });
  const first = await syncGlobalAssets(storage, [source('one')]);
  const second = await syncGlobalAssets(storage, [source('two')]);
  assert.deepEqual(second.orphanedKeys, [first.assets['product-template']?.key]);
});
