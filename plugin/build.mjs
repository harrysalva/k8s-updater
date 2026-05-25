// Direct Vite build script — bypasses the broken headlamp-plugin CLI (yargs/ESM conflict on Node v26).
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';
import { readFileSync } from 'fs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

// Resolve modules from plugin's node_modules
const pluginLibPath = resolve(__dirname, 'node_modules/@kinvolk/headlamp-plugin');

const { default: viteConfig } = await import(
  resolve(pluginLibPath, 'config/vite.config.mjs')
);
const { build } = await import(resolve(pluginLibPath, 'node_modules/vite/dist/node/index.js'));
const { pluginNameInjection } = await import(
  resolve(pluginLibPath, 'config/vite-plugin-name-injection.mjs')
);

const packageJson = JSON.parse(readFileSync(resolve(__dirname, 'package.json'), 'utf8'));
const pluginName = packageJson.name;

process.env.NODE_ENV = process.env.NODE_ENV || 'production';

const buildConfig = { ...viteConfig };
if (!buildConfig.plugins) buildConfig.plugins = [];
buildConfig.plugins.push(pluginNameInjection({ pluginName }));

// Override output dir to dist/
buildConfig.build = {
  ...buildConfig.build,
  outDir: resolve(__dirname, 'dist'),
};

console.log(`Building plugin "${pluginName}"...`);

try {
  await build(buildConfig);
  console.log('Build complete → dist/main.js');
} catch (e) {
  console.error(e);
  process.exit(1);
}
