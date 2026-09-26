import { describe, expect, it } from "vitest";
import {
  isCouncilShow,
  readDeliberation,
  writeDeliberation,
} from "./useCouncil";

describe("isCouncilShow", () => {
  it("is true only for an enabled council", () => {
    expect(isCouncilShow({ xollama: { council: { enabled: true } } })).toBe(
      true,
    );
    expect(isCouncilShow({ xollama: { council: { enabled: false } } })).toBe(
      false,
    );
    expect(isCouncilShow({ xollama: { council: {} } })).toBe(false);
    expect(isCouncilShow({ xollama: {} })).toBe(false);
    expect(isCouncilShow({ capabilities: ["completion"] })).toBe(false);
    expect(isCouncilShow(null)).toBe(false);
  });
});

function memory() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
}

describe("deliberation preference", () => {
  it("defaults to on, as the council's show_deliberation does", () => {
    expect(readDeliberation(memory())).toBe(true);
  });

  it("remembers off and on", () => {
    const s = memory();
    writeDeliberation(false, s);
    expect(readDeliberation(s)).toBe(false);
    writeDeliberation(true, s);
    expect(readDeliberation(s)).toBe(true);
  });

  it("stays on when storage throws", () => {
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    };
    expect(() => writeDeliberation(false, broken)).not.toThrow();
    expect(readDeliberation(broken)).toBe(true);
  });
});
