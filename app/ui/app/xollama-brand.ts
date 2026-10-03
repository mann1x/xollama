// The desktop app's product name, rewritten at build time (the app-brand hook,
// docs/features/rebrand.md). Upstream's UI names the product in some 170
// strings; editing each would conflict on every upstream sync, so the build
// rewrites them instead, and the source stays upstream's.
//
// "Ollama" becomes "xOllama" where it names the app. It stays where it names
// ollama.com: "Ollama account", "Ollama.com". Identifiers are left alone by
// the word boundary (useOllama, OllamaModel) and by skipping the one file that
// uses ollama-js's `Ollama` class.
import type { Plugin } from "vite";

export const appName = "xOllama";

const product = /\bOllama\b(?!\.com|\s+[Aa]ccount)/g;

export function rebrand(code: string): string {
  return code.replace(product, appName);
}

// Only the app's own sources: never node_modules, never the client module.
function ours(id: string): boolean {
  const path = id.split("?")[0].replace(/\\/g, "/");
  return (
    /\/src\/.*\.(ts|tsx)$/.test(path) &&
    !path.includes("/node_modules/") &&
    !path.endsWith("/src/lib/ollama-client.ts") &&
    !/\.test\.tsx?$/.test(path)
  );
}

export function xollamaBrand(): Plugin {
  return {
    name: "xollama-brand",
    apply: "build",
    enforce: "pre",
    transform(code, id) {
      return ours(id) ? { code: rebrand(code), map: null } : null;
    },
    transformIndexHtml(html) {
      return html.replace(/<title>Ollama<\/title>/, `<title>${appName}</title>`);
    },
  };
}
