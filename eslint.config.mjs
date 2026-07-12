import rootConfig from '../../eslint.config.mjs';

export default [
  ...rootConfig,
  {
    files: ['test/**/*.ts'],
    rules: {
      '@typescript-eslint/no-base-to-string': 'off',
      '@typescript-eslint/no-floating-promises': 'off',
      '@typescript-eslint/require-await': 'off',
    },
  },
  {
    files: ['scripts/**/*.mjs'],
    rules: {
      '@typescript-eslint/prefer-nullish-coalescing': 'off',
    },
  },
];
