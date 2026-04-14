# Site

Keep the landing page content-driven and Astro-native.

- Treat `site/CONTENT.md` as the source for page copy; update the parser in `src/pages/index.astro` when section structure changes.
- Keep page assembly in `src/pages/index.astro` and keep `src/components/*.astro` presentational with typed `Astro.props`.
- Use the existing Astro + Tailwind toolchain from `site/package.json`, `astro.config.mjs`, and `tailwind.config.cjs`; do not introduce React-only patterns here.
- Preserve the current bold visual language unless the task explicitly changes the design direction.
- Verify with `npm run build` from `site/`.
