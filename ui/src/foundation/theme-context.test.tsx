import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  ColorModeProvider,
  DARK_TEAL_SELECTION_COLORS,
  DEFAULT_SELECTION_COLORS,
  WARM_STONE_SELECTION_COLORS,
  useColorMode,
} from "./theme-context";

function ThemeHarness() {
  const {
    accentColor,
    accentColors,
    colorMode,
    resetAccentColor,
    selectionColors,
    setAccentColor,
    setColorMode,
    setSelectionColor,
    setSelectionPalette,
    toggleColorMode,
  } = useColorMode();

  return (
    <>
      <button type="button" onClick={toggleColorMode}>
        {colorMode}
      </button>
      <button type="button" onClick={() => setColorMode("dark")}>
        show dark
      </button>
      <button type="button" onClick={() => setColorMode("light")}>
        show light
      </button>
      <button type="button" onClick={() => setAccentColor("light", "#ff00aa")}>
        light {accentColors.light ?? "default accent"}
      </button>
      <button type="button" onClick={() => setAccentColor("dark", "#00ffaa")}>
        dark {accentColors.dark ?? "default accent"}
      </button>
      <button type="button" onClick={() => resetAccentColor("light")}>
        reset light accent
      </button>
      <button type="button" onClick={() => resetAccentColor("dark")}>
        reset dark accent
      </button>
      <button
        type="button"
        onClick={() => setSelectionColor("light", "selectedBg", "#f7efee")}
      >
        selection {selectionColors.light.selectedBg}
      </button>
      <button
        type="button"
        onClick={() =>
          setSelectionPalette({
            light: { ...DEFAULT_SELECTION_COLORS.light, selectedBg: "#f4f2ef" },
            dark: { ...DEFAULT_SELECTION_COLORS.dark, selectedBg: "#171717" },
          })
        }
      >
        preset selection
      </button>
      <output aria-label="active accent">
        {accentColor ?? "default accent"}
      </output>
    </>
  );
}

function renderThemeHarness() {
  render(<ColorModeProvider>{() => <ThemeHarness />}</ColorModeProvider>);
}

describe("ColorModeProvider", () => {
  afterEach(() => {
    cleanup();
  });

  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
    document.documentElement.removeAttribute("style");
  });

  it("reads the saved color mode and writes updates back to local storage", () => {
    window.localStorage.setItem("afs_color_mode", "dark");

    renderThemeHarness();

    expect(screen.getByRole("button", { name: "dark" })).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");

    fireEvent.click(screen.getByRole("button", { name: "dark" }));

    expect(screen.getByRole("button", { name: "light" })).toBeInTheDocument();
    expect(window.localStorage.getItem("afs_color_mode")).toBe("light");
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });

  it("applies the saved accent color for the active theme", () => {
    window.localStorage.setItem("afs_color_mode", "dark");
    window.localStorage.setItem(
      "afs_accent_color",
      JSON.stringify({ light: "#ff00aa", dark: "#00ffaa" }),
    );

    renderThemeHarness();

    expect(screen.getByRole("button", { name: "dark" })).toBeInTheDocument();
    expect(screen.getByLabelText("active accent")).toHaveTextContent("#00ffaa");
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("#00ffaa");
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent-soft"),
    ).toBe("rgba(0, 255, 170, 0.12)");

    fireEvent.click(screen.getByRole("button", { name: "show light" }));

    expect(screen.getByLabelText("active accent")).toHaveTextContent("#ff00aa");
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("#ff00aa");
  });

  it("updates and resets accent colors independently", () => {
    renderThemeHarness();

    fireEvent.click(
      screen.getByRole("button", { name: "light default accent" }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "dark default accent" }),
    );

    expect(
      screen.getByRole("button", { name: "light #ff00aa" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "dark #00ffaa" }),
    ).toBeInTheDocument();
    expect(
      JSON.parse(window.localStorage.getItem("afs_accent_color") ?? "{}"),
    ).toEqual({
      light: "#ff00aa",
      dark: "#00ffaa",
    });

    fireEvent.click(screen.getByRole("button", { name: "reset light accent" }));

    expect(
      screen.getByRole("button", { name: "light default accent" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "dark #00ffaa" }),
    ).toBeInTheDocument();
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("");
    expect(
      JSON.parse(window.localStorage.getItem("afs_accent_color") ?? "{}"),
    ).toEqual({
      light: null,
      dark: "#00ffaa",
    });

    fireEvent.click(screen.getByRole("button", { name: "show dark" }));

    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("#00ffaa");
  });

  it("migrates the legacy single accent color to both themes", () => {
    window.localStorage.setItem("afs_accent_color", "#ff00aa");

    renderThemeHarness();

    expect(
      screen.getByRole("button", { name: "light #ff00aa" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "dark #ff00aa" }),
    ).toBeInTheDocument();
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("#ff00aa");

    fireEvent.click(screen.getByRole("button", { name: "show dark" }));

    expect(screen.getByLabelText("active accent")).toHaveTextContent("#ff00aa");
    expect(
      document.documentElement.style.getPropertyValue("--afs-accent"),
    ).toBe("#ff00aa");
  });

  it("applies and stores selection palette colors", () => {
    renderThemeHarness();

    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-bg"),
    ).toBe(DEFAULT_SELECTION_COLORS.light.selectedBg);
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-indicator",
      ),
    ).toBe(DEFAULT_SELECTION_COLORS.light.indicator);
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-hover-ink",
      ),
    ).toBe(DEFAULT_SELECTION_COLORS.light.hoverInk);
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-text"),
    ).toBe(DEFAULT_SELECTION_COLORS.light.selectedText);
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-ink"),
    ).toBe(DEFAULT_SELECTION_COLORS.light.selectedText);

    fireEvent.click(
      screen.getByRole("button", {
        name: `selection ${DEFAULT_SELECTION_COLORS.light.selectedBg}`,
      }),
    );

    expect(
      screen.getByRole("button", { name: "selection #f7efee" }),
    ).toBeInTheDocument();
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-bg"),
    ).toBe("#f7efee");

    fireEvent.click(screen.getByRole("button", { name: "preset selection" }));

    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-bg"),
    ).toBe("#f4f2ef");
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-hover-ink",
      ),
    ).toBe(DEFAULT_SELECTION_COLORS.light.hoverInk);
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-text"),
    ).toBe(DEFAULT_SELECTION_COLORS.light.selectedText);
    expect(
      JSON.parse(window.localStorage.getItem("afs_selection_palette") ?? "{}"),
    ).toMatchObject({
      light: { selectedBg: "#f4f2ef" },
      dark: { selectedBg: "#171717" },
    });
  });

  it("migrates the old dark teal tint indicator to teal", () => {
    window.localStorage.setItem("afs_color_mode", "dark");
    window.localStorage.setItem(
      "afs_selection_palette_version",
      "chartreuse-light-dark-v1",
    );
    window.localStorage.setItem(
      "afs_selection_palette",
      JSON.stringify({
        light: { ...DARK_TEAL_SELECTION_COLORS.light, indicator: "#ff4438" },
        dark: { ...DARK_TEAL_SELECTION_COLORS.dark, indicator: "#ff4438" },
      }),
    );

    renderThemeHarness();

    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-indicator",
      ),
    ).toBe(DARK_TEAL_SELECTION_COLORS.dark.indicator);
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-hover-ink",
      ),
    ).toBe(DARK_TEAL_SELECTION_COLORS.dark.hoverInk);
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-text"),
    ).toBe(DARK_TEAL_SELECTION_COLORS.dark.selectedText);
    expect(
      JSON.parse(window.localStorage.getItem("afs_selection_palette") ?? "{}"),
    ).toMatchObject({
      light: { indicator: DARK_TEAL_SELECTION_COLORS.light.indicator },
      dark: { indicator: DARK_TEAL_SELECTION_COLORS.dark.indicator },
    });
  });

  it("migrates the previous default selection palette to chartreuse", () => {
    window.localStorage.setItem(
      "afs_selection_palette",
      JSON.stringify(WARM_STONE_SELECTION_COLORS),
    );

    renderThemeHarness();

    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-bg"),
    ).toBe(DEFAULT_SELECTION_COLORS.light.selectedBg);
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-indicator",
      ),
    ).toBe(DEFAULT_SELECTION_COLORS.light.indicator);
    expect(
      JSON.parse(window.localStorage.getItem("afs_selection_palette") ?? "{}"),
    ).toMatchObject({
      light: { selectedBg: DEFAULT_SELECTION_COLORS.light.selectedBg },
      dark: { selectedBg: DEFAULT_SELECTION_COLORS.dark.selectedBg },
    });
  });

  it("migrates legacy selected ink into selected text", () => {
    window.localStorage.setItem(
      "afs_selection_palette",
      JSON.stringify({
        light: { selectedInk: "#123456" },
        dark: { selectedInk: "#abcdef" },
      }),
    );

    renderThemeHarness();

    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-text"),
    ).toBe("#123456");
    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-ink"),
    ).toBe("#123456");
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-hover-ink",
      ),
    ).toBe("#123456");

    fireEvent.click(screen.getByRole("button", { name: "show dark" }));

    expect(
      document.documentElement.style.getPropertyValue("--afs-selection-text"),
    ).toBe("#abcdef");
    expect(
      document.documentElement.style.getPropertyValue(
        "--afs-selection-hover-ink",
      ),
    ).toBe("#abcdef");
    expect(
      JSON.parse(window.localStorage.getItem("afs_selection_palette") ?? "{}"),
    ).toMatchObject({
      light: { selectedText: "#123456" },
      dark: { selectedText: "#abcdef" },
    });
  });
});
