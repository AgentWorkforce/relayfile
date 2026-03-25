/** @type {import('tailwindcss').Config} */
module.exports = {
  darkMode: "class",
  content: ["./src/**/*.{astro,html,js,jsx,md,mdx,svelte,ts,tsx,vue}"],
  theme: {
    extend: {
      colors: {
        brand: {
          bg: "#09090b",
          panel: "#18181b",
          accent: "#22d3ee",
          accentStrong: "#06b6d4"
        }
      },
      boxShadow: {
        glow: "0 0 0 1px rgba(34, 211, 238, 0.18), 0 18px 60px rgba(8, 145, 178, 0.18)"
      },
      backgroundImage: {
        "hero-grid":
          "radial-gradient(circle at top, rgba(34, 211, 238, 0.18), transparent 36%), linear-gradient(rgba(24, 24, 27, 0.4) 1px, transparent 1px), linear-gradient(90deg, rgba(24, 24, 27, 0.4) 1px, transparent 1px)"
      }
    }
  },
  plugins: []
};
