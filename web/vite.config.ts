import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// During `npm run dev`, API calls go to a slskdpp running on :5030.
export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: { '/api': { target: 'http://localhost:5030', changeOrigin: false } },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    // The content security policy allows fonts from 'self' only, so they
    // must stay files rather than data: URLs.
    assetsInlineLimit: (file) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
  },
});
