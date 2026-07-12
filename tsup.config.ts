import { defineConfig } from 'tsup';

export default defineConfig([
  {
    entry: {
      index: 'src/index.ts',
      'server/index': 'src/server/index.ts',
      'client/index': 'src/client/index.ts',
      'react/index': 'src/react/index.ts',
      'expo/index': 'src/expo/index.ts',
      'hono/index': 'src/hono/index.ts',
      'testing/index': 'src/testing/index.ts',
    },
    clean: true,
    dts: true,
    format: ['esm', 'cjs'],
    sourcemap: true,
    splitting: false,
    target: 'node20',
    external: ['react', 'hono', 'expo-file-system'],
  },
  {
    entry: { cli: 'src/cli.ts' },
    clean: false,
    dts: false,
    format: ['esm'],
    sourcemap: true,
    target: 'node20',
  },
]);
