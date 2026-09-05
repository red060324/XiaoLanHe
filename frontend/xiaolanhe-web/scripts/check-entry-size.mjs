import { readFile, stat } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const projectDirectory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const indexPath = path.join(projectDirectory, 'dist', 'index.html');
const indexHTML = await readFile(indexPath, 'utf8');
const entryMatch = indexHTML.match(/<script[^>]+type="module"[^>]+src="([^"]+)"/);

if (!entryMatch) {
  throw new Error('Unable to locate the production module entry in dist/index.html');
}

const entryRelativePath = entryMatch[1].startsWith('/') ? entryMatch[1].slice(1) : entryMatch[1];
const entryPath = path.resolve(projectDirectory, 'dist', entryRelativePath);
const distDirectory = path.resolve(projectDirectory, 'dist');
if (entryPath !== distDirectory && !entryPath.startsWith(`${distDirectory}${path.sep}`)) {
  throw new Error(`Production entry resolves outside dist: ${entryMatch[1]}`);
}

const maximumEntryBytes = 500 * 1024;
const entryStat = await stat(entryPath);
if (!entryStat.isFile()) {
  throw new Error(`Production entry is not a file: ${entryRelativePath}`);
}
if (entryStat.size > maximumEntryBytes) {
  throw new Error(
    `Production entry ${entryRelativePath} is ${entryStat.size} bytes; maximum is ${maximumEntryBytes} bytes`,
  );
}

console.log(`Production entry budget: ${entryRelativePath} ${entryStat.size}/${maximumEntryBytes} bytes`);
