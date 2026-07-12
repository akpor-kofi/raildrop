import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

for (const file of [
  'dist/index.js',
  'dist/client/index.js',
  'dist/react/index.js',
  'dist/expo/index.js',
]) {
  const source = await readFile(new URL(`../${file}`, import.meta.url), 'utf8');
  assert.doesNotMatch(source, /@aws-sdk|AWS_ACCESS_KEY|SECRET_ACCESS_KEY|S3Client/);
}
