import { build } from '../../tools/build.mjs';

if (process.env.REMOTA_BINARY_DIR === undefined) {
  process.env.GOOS = 'linux';
  process.env.GOARCH = process.arch === 'arm64' ? 'arm64' : 'amd64';
  build();
} else if (!process.env.REMOTA_BINARY_DIR.trim()) {
  throw new Error('REMOTA_BINARY_DIR must identify the binaries to accept');
}
