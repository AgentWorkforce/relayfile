#!/usr/bin/env node
// Finalize each package's CHANGELOG.md for a release:
//   - rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`
//   - insert a fresh empty `## [Unreleased]` section above it
//   - rewrite the `[Unreleased]: ...compare/<prev>...HEAD` link to start from the new tag
//   - append a `[X.Y.Z]: ...releases/tag/vX.Y.Z` link reference
//
// Usage: node scripts/finalize-changelogs.mjs <version>
//   e.g. node scripts/finalize-changelogs.mjs 0.3.3

import fs from 'node:fs';

const version = process.argv[2];
if (!version) {
  console.error('Usage: finalize-changelogs.mjs <version>');
  process.exit(1);
}

const today = new Date().toISOString().slice(0, 10);
const repoSlug = process.env.GITHUB_REPOSITORY || 'AgentWorkforce/relayfile';

const changelogs = [
  'packages/core/CHANGELOG.md',
  'packages/sdk/typescript/CHANGELOG.md',
  'packages/cli/CHANGELOG.md',
  'packages/file-observer/CHANGELOG.md',
  'packages/local-mount/CHANGELOG.md',
];

const placeholder = '_No unreleased changes._';
const unreleasedHeadingRe = /^## \[Unreleased\][^\n]*$/m;
const unreleasedLinkRe =
  /^\[Unreleased\]:\s*https:\/\/github\.com\/[^\s/]+\/[^\s/]+\/compare\/\S+?\.\.\.HEAD\s*$/m;

for (const clPath of changelogs) {
  if (!fs.existsSync(clPath)) {
    console.log(`skip ${clPath}: missing`);
    continue;
  }
  let text = fs.readFileSync(clPath, 'utf8');

  if (!unreleasedHeadingRe.test(text)) {
    console.log(`skip ${clPath}: no [Unreleased] heading`);
    continue;
  }

  const versionHeading = `## [${version}] - ${today}`;
  text = text.replace(
    unreleasedHeadingRe,
    ['## [Unreleased]', '', placeholder, '', versionHeading].join('\n')
  );

  // If the newly-created version block inherited only the placeholder, reword
  // it so the release entry reads naturally instead of "No unreleased changes"
  // under a dated heading.
  const inheritedEmpty = `${versionHeading}\n\n${placeholder}`;
  const rewritten = `${versionHeading}\n\n_No user-visible changes in this release._`;
  if (text.includes(inheritedEmpty)) {
    text = text.replace(inheritedEmpty, rewritten);
  }

  const newCompare = `[Unreleased]: https://github.com/${repoSlug}/compare/v${version}...HEAD`;
  const releaseLink = `[${version}]: https://github.com/${repoSlug}/releases/tag/v${version}`;

  if (unreleasedLinkRe.test(text)) {
    text = text.replace(unreleasedLinkRe, `${newCompare}\n${releaseLink}`);
  } else {
    text = text.replace(/\s*$/, `\n\n${newCompare}\n${releaseLink}\n`);
  }

  fs.writeFileSync(clPath, text);
  console.log(`updated ${clPath} -> [${version}] ${today}`);
}
