import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config';

// Dates render in local time; pin the zone so date assertions hold on any machine.
process.env.TZ = 'UTC';

export default mergeConfig(viteConfig, defineConfig({
  // The flows type into several fields; a busy machine can take them past the 5 s default.
  test: { environment: 'jsdom', setupFiles: ['./vitest.setup.ts'], globals: true, css: false, exclude: ['e2e/**', 'node_modules/**'], testTimeout: 15_000 },
}));
