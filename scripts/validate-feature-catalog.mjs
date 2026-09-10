#!/usr/bin/env node
import { existsSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parse } from 'yaml';

import { collectPublicSurfaces } from './feature-catalog-surfaces.mjs';

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
export const DEFAULT_ROOT = resolve(SCRIPT_DIR, '..');
export const DEFAULT_MANIFEST = '.agentworkforce/features/manifest.yaml';
const REQUIRED_PROCEDURE_PARTS = [
  'Prerequisites',
  'Isolated setup',
  'Commands',
  'Positive assertions',
  'Negative assertions',
  'Cleanup',
  'Automation limits',
  'Reporting',
];

function ownRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function secureRelativePath(root, value, label, errors, mustExist = true) {
  if (typeof value !== 'string' || value.trim() === '') {
    errors.push(`${label} must be a non-empty relative path`);
    return null;
  }
  if (isAbsolute(value) || value.split(/[\\/]/).includes('..')) {
    errors.push(`${label} escapes the repository: ${value}`);
    return null;
  }
  const absolute = resolve(root, value);
  const rel = relative(root, absolute);
  if (rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) {
    errors.push(`${label} escapes the repository: ${value}`);
    return null;
  }
  if (mustExist && !existsSync(absolute)) {
    errors.push(`${label} does not exist: ${value}`);
    return null;
  }
  if (mustExist) {
    const canonicalRoot = realpathSync(root);
    const canonical = realpathSync(absolute);
    const canonicalRel = relative(canonicalRoot, canonical);
    if (canonicalRel === '..' || canonicalRel.startsWith(`..${sep}`) || isAbsolute(canonicalRel)) {
      errors.push(`${label} resolves outside the repository: ${value}`);
      return null;
    }
  }
  return absolute;
}

function globPattern(pattern) {
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replaceAll('*', '.*');
  return new RegExp(`^${escaped}$`);
}

function sectionForProcedure(document, name) {
  const lines = document.split(/\r?\n/);
  const start = lines.findIndex((line) => line === `## ${name}`);
  if (start < 0) return null;
  const relativeEnd = lines.slice(start + 1).findIndex((line) => line.startsWith('## '));
  const end = relativeEnd < 0 ? lines.length : start + 1 + relativeEnd;
  return lines.slice(start + 1, end).join('\n');
}

export function computeSummary(manifest) {
  const categories = Object.values(manifest.categories ?? {});
  const features = categories.flatMap((category) => category.features ?? []);
  const criticality = { critical: 0, hot: 0, standard: 0 };
  const verify_tiers = { '1': 0, '2': 0, '3': 0, '4': 0, '5': 0, '6': 0 };
  for (const category of categories) {
    if (Object.hasOwn(criticality, category.criticality)) {
      criticality[category.criticality] += (category.features ?? []).length;
    }
    for (const feature of category.features ?? []) {
      if (Object.hasOwn(verify_tiers, String(feature.verify_tier))) {
        verify_tiers[String(feature.verify_tier)] += 1;
      }
    }
  }
  return { categories: categories.length, features: features.length, criticality, verify_tiers };
}

export function validateManifestData(manifest, options = {}) {
  const root = resolve(options.root ?? DEFAULT_ROOT);
  const structuralOnly = options.structuralOnly === true;
  const errors = [];
  if (!ownRecord(manifest)) return ['manifest must be a mapping'];
  if (String(manifest.version) !== '1.1') errors.push('manifest version must be 1.1');
  if (typeof manifest.updated !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(manifest.updated)) {
    errors.push('manifest updated must be an ISO calendar date');
  }
  if (!ownRecord(manifest.categories) || Object.keys(manifest.categories).length === 0) {
    errors.push('manifest categories must be a non-empty mapping');
  }
  if (!ownRecord(manifest.verification) || !ownRecord(manifest.verification?.categories)) {
    errors.push('verification.categories must be a mapping');
  }

  const categoryNames = Object.keys(manifest.categories ?? {}).sort();
  const routeNames = Object.keys(manifest.verification?.categories ?? {}).sort();
  for (const category of categoryNames) {
    if (!routeNames.includes(category)) errors.push(`verification route missing for category ${category}`);
  }
  for (const route of routeNames) {
    if (!categoryNames.includes(route)) errors.push(`verification route references unknown category ${route}`);
  }

  const documentPath = secureRelativePath(
    root,
    manifest.verification?.document,
    'verification.document',
    errors,
    options.proceduresText === undefined && !structuralOnly
  );
  let procedures = options.proceduresText;
  if (!structuralOnly && procedures === undefined && documentPath) procedures = readFileSync(documentPath, 'utf8');
  const proceduresSeen = new Set();
  for (const [category, procedure] of Object.entries(manifest.verification?.categories ?? {})) {
    if (typeof procedure !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(procedure)) {
      errors.push(`verification route for ${category} is not a named procedure`);
      continue;
    }
    if (proceduresSeen.has(procedure)) errors.push(`procedure route is duplicated: ${procedure}`);
    proceduresSeen.add(procedure);
    if (typeof procedures === 'string') {
      const section = sectionForProcedure(procedures, procedure);
      if (section === null) {
        errors.push(`procedure heading missing: ## ${procedure}`);
      } else {
        for (const part of REQUIRED_PROCEDURE_PARTS) {
          if (!new RegExp(`^### ${part}\\s*$`, 'm').test(section)) {
            errors.push(`procedure ${procedure} is missing ### ${part}`);
          }
        }
        if (!/\b(PASS|FAIL|SKIP|MANUAL)\b/.test(section)) {
          errors.push(`procedure ${procedure} lacks explicit outcome semantics`);
        }
      }
    }
  }

  const ids = new Set();
  const coverEntries = [];
  for (const [categoryId, category] of Object.entries(manifest.categories ?? {})) {
    if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(categoryId)) errors.push(`invalid category id ${categoryId}`);
    if (!ownRecord(category)) {
      errors.push(`category ${categoryId} must be a mapping`);
      continue;
    }
    if (typeof category.name !== 'string' || category.name.trim() === '') errors.push(`category ${categoryId} needs a name`);
    if (typeof category.description !== 'string' || category.description.trim() === '') errors.push(`category ${categoryId} needs a description`);
    if (!['critical', 'hot', 'standard'].includes(category.criticality)) errors.push(`category ${categoryId} has invalid criticality`);
    if (!Array.isArray(category.features) || category.features.length === 0) {
      errors.push(`category ${categoryId} must contain features`);
      continue;
    }
    for (const feature of category.features) {
      const label = `${categoryId}/${feature?.id ?? '<missing-id>'}`;
      if (!ownRecord(feature)) {
        errors.push(`${label} must be a mapping`);
        continue;
      }
      if (typeof feature.id !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(feature.id)) {
        errors.push(`${label} has an invalid stable id`);
      } else if (ids.has(feature.id)) {
        errors.push(`duplicate feature id ${feature.id}`);
      } else {
        ids.add(feature.id);
      }
      if (typeof feature.name !== 'string' || feature.name.trim() === '') errors.push(`${label} needs a name`);
      if (typeof feature.description !== 'string' || feature.description.trim() === '') errors.push(`${label} needs a truthful description`);
      if (!Number.isInteger(feature.verify_tier) || feature.verify_tier < 1 || feature.verify_tier > 6) {
        errors.push(`${label} has invalid verify_tier`);
      }
      const locations = Array.isArray(feature.locations)
        ? feature.locations
        : typeof feature.location === 'string'
          ? [feature.location]
          : [];
      if (locations.length === 0) errors.push(`${label} needs at least one implementation location`);
      for (const location of locations) {
        secureRelativePath(root, location, `${label} location`, errors, !structuralOnly);
      }
      if (!Array.isArray(feature.covers) || feature.covers.length === 0) {
        errors.push(`${label} needs public surface coverage`);
      } else {
        for (const pattern of feature.covers) {
          if (typeof pattern !== 'string' || pattern.trim() === '') errors.push(`${label} has an invalid coverage selector`);
          else coverEntries.push({ categoryId, featureId: feature.id, pattern });
        }
      }
    }
  }

  const expectedSummary = computeSummary(manifest);
  if (JSON.stringify(manifest.summary) !== JSON.stringify(expectedSummary)) {
    errors.push(`stale summary totals: expected ${JSON.stringify(expectedSummary)}`);
  }

  const exclusions = new Map();
  const rawExclusions = manifest.surface_exclusions ?? [];
  if (!Array.isArray(rawExclusions)) errors.push('surface_exclusions must be a sequence');
  for (const exclusion of Array.isArray(rawExclusions) ? rawExclusions : []) {
    if (!ownRecord(exclusion) || typeof exclusion.surface !== 'string' || typeof exclusion.reason !== 'string' || exclusion.reason.trim() === '') {
      errors.push('surface exclusions require exact surface and non-empty reason');
      continue;
    }
    if (exclusion.surface.includes('*')) errors.push(`surface exclusion must be exact: ${exclusion.surface}`);
    if (exclusions.has(exclusion.surface)) errors.push(`duplicate surface exclusion: ${exclusion.surface}`);
    exclusions.set(exclusion.surface, exclusion.reason);
  }
  if (structuralOnly) return errors;

  const publicSurfaces = options.publicSurfaces ?? collectPublicSurfaces(root);
  for (const excluded of exclusions.keys()) {
    if (!publicSurfaces.includes(excluded)) errors.push(`surface exclusion is stale or unknown: ${excluded}`);
  }

  const usedPatterns = new Map(coverEntries.map((entry) => [`${entry.featureId}\0${entry.pattern}`, 0]));
  for (const surface of publicSurfaces) {
    const matches = coverEntries.filter((entry) => globPattern(entry.pattern).test(surface));
    if (exclusions.has(surface)) {
      if (matches.length > 0) errors.push(`excluded public surface is also covered: ${surface}`);
      continue;
    }
    if (matches.length === 0) errors.push(`public surface omission: ${surface}`);
    if (matches.length > 1) {
      errors.push(`public surface has overlapping feature coverage: ${surface} (${matches.map((m) => m.featureId).join(', ')})`);
    }
    for (const match of matches) {
      const key = `${match.featureId}\0${match.pattern}`;
      usedPatterns.set(key, (usedPatterns.get(key) ?? 0) + 1);
    }
  }
  for (const entry of coverEntries) {
    if ((usedPatterns.get(`${entry.featureId}\0${entry.pattern}`) ?? 0) === 0) {
      errors.push(`coverage selector matches no public surface: ${entry.featureId} -> ${entry.pattern}`);
    }
  }
  return errors;
}

export function loadAndValidateManifest(options = {}) {
  const root = resolve(options.root ?? DEFAULT_ROOT);
  const manifestPath = secureRelativePath(root, options.manifestPath ?? DEFAULT_MANIFEST, 'manifest', [], true);
  if (!manifestPath) throw new Error('manifest path is invalid or missing');
  const manifest = parse(readFileSync(manifestPath, 'utf8'));
  return { manifest, errors: validateManifestData(manifest, { ...options, root }) };
}

function main() {
  const { manifest, errors } = loadAndValidateManifest();
  if (errors.length > 0) {
    console.error('FEATURE_CATALOG_FAIL');
    for (const error of errors) console.error(`- ${error}`);
    process.exitCode = 1;
    return;
  }
  const summary = computeSummary(manifest);
  console.log(`FEATURE_CATALOG_PASS categories=${summary.categories} features=${summary.features}`);
  console.log(`criticality=${JSON.stringify(summary.criticality)} tiers=${JSON.stringify(summary.verify_tiers)}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
