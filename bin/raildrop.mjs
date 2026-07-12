#!/usr/bin/env node

const compiledCliUrl = new URL('../dist/cli.js', import.meta.url);

try {
  await import(compiledCliUrl.href);
} catch (error) {
  const isMissingCompiledCli =
    error instanceof Error &&
    'code' in error &&
    error.code === 'ERR_MODULE_NOT_FOUND' &&
    error.message.includes('/dist/cli.js');

  if (!isMissingCompiledCli) throw error;

  process.stderr.write(
    'Raildrop CLI has not been built. Run "pnpm --filter raildrop build" and try again.\n'
  );
  process.exitCode = 1;
}
