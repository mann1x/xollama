import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "fs";
import { join } from "path";
import { rebrand, xollamaBrand } from "./xollama-brand";

describe("xollama-brand", () => {
  it("names the app xOllama and leaves ollama.com alone", () => {
    expect(rebrand("Welcome to Ollama!")).toBe("Welcome to xOllama!");
    expect(rebrand("Expose Ollama to the network")).toBe("Expose xOllama to the network");
    expect(rebrand("Connecting Claude to Ollama…")).toBe("Connecting Claude to xOllama…");
    expect(rebrand("Cloud models require an Ollama account")).toBe("Cloud models require an Ollama account");
    expect(rebrand("Connect Ollama Account")).toBe("Connect Ollama Account");
    expect(rebrand("available to your Ollama.com account")).toBe("available to your Ollama.com account");
    expect(rebrand("useOllama(); OllamaModel; ollama.chat")).toBe("useOllama(); OllamaModel; ollama.chat");
  });

  it("rewrites the window title and only the app's own sources", () => {
    const p = xollamaBrand();
    const html = (p.transformIndexHtml as (h: string) => string)("<title>Ollama</title>");
    expect(html).toBe("<title>xOllama</title>");
    const t = p.transform as (code: string, id: string) => { code: string } | null;
    expect(t('"Run Ollama"', "/a/app/src/components/Onboarding.tsx")?.code).toBe('"Run xOllama"');
    expect(t('import { Ollama } from "ollama/browser"', "/a/app/src/lib/ollama-client.ts")).toBeNull();
    expect(t('"Ollama"', "/a/app/node_modules/ollama/src/x.ts")).toBeNull();
  });

  // Every bare `Ollama` in the sources must be text: a new identifier named
  // Ollama anywhere but the client module would be renamed by the build.
  it("finds Ollama used as code only in the client module", () => {
    const files: string[] = [];
    const walk = (d: string) => {
      for (const f of readdirSync(d)) {
        const p = join(d, f);
        if (statSync(p).isDirectory()) walk(p);
        else if (/\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f)) files.push(p);
      }
    };
    walk(join(__dirname, "src"));
    const code = /(\bnew\s+Ollama\b|\bas\s+Ollama\b|:\s*Ollama\b|<Ollama\b|\{\s*Ollama\s*\}|keyof\s+Ollama\b|\bOllama\s*\()/;
    const offenders = files.filter((f) => !f.endsWith("ollama-client.ts") && code.test(readFileSync(f, "utf8")));
    expect(offenders).toEqual([]);
  });
});
