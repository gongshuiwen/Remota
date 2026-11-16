import { build } from '../../tools/build.mjs';

process.env.GOOS = 'linux';
process.env.GOARCH = process.arch === 'arm64' ? 'arm64' : 'amd64';
build();
