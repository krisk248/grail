import { sveltekit } from '@sveltejs/kit/vite';

export default {
  plugins: [sveltekit()],
  server: {
    proxy: {
      '/api':       'http://localhost:8080',
      '/admin':     'http://localhost:8080'
    }
  }
};
