import { build } from 'vite';
import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

async function buildProduction() {
  try {
    await build({
      root: resolve(__dirname),
      mode: 'production',
      build: {
        rollupOptions: {
          onwarn(warning, warn) {
            // Suppress the specific error we're encountering
            if (warning.message?.includes('Cannot add property')) {
              return;
            }
            warn(warning);
          },
          // Use a more conservative optimization
          output: {
            manualChunks: {
              'vendor': ['vue', 'vue-router', 'pinia'],
              'ui': ['@arco-design/web-vue'],
              'utils': ['axios', 'js-yaml', 'codemirror']
            }
          }
        }
      }
    });
    
    console.log('Build completed successfully!');
  } catch (error) {
    console.error('Build failed:', error);
    process.exit(1);
  }
}

buildProduction();