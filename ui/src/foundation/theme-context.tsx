import type { ReactNode } from "react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

export type ColorMode = "light" | "dark";
export type AccentColors = Record<ColorMode, string | null>;
export type SelectionColorSlot =
  | "selectedBg"
  | "hoverBg"
  | "hoverInk"
  | "selectedBorder"
  | "selectedText"
  | "indicator";
export type SelectionPalette = Record<SelectionColorSlot, string> & {
  indicatorWidth: string;
};
export type SelectionPalettes = Record<ColorMode, SelectionPalette>;

interface ThemeContextValue {
  colorMode: ColorMode;
  setColorMode: (colorMode: ColorMode) => void;
  toggleColorMode: () => void;
  accentColor: string | null;
  accentColors: AccentColors;
  setAccentColor: (colorMode: ColorMode, accentColor: string) => void;
  resetAccentColor: (colorMode: ColorMode) => void;
  selectionColors: SelectionPalettes;
  setSelectionColor: (
    colorMode: ColorMode,
    slot: SelectionColorSlot,
    color: string,
  ) => void;
  setSelectionPalette: (selectionColors: SelectionPalettes) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

const STORAGE_KEY = "afs_color_mode";
const ACCENT_STORAGE_KEY = "afs_accent_color";
const SELECTION_STORAGE_KEY = "afs_selection_palette";
const SELECTION_VERSION_STORAGE_KEY = "afs_selection_palette_version";
const CURRENT_SELECTION_VERSION = "chartreuse-light-dark-v1";
export const DEFAULT_ACCENT_COLOR = "#064ea2";
const VALID_COLOR_MODES: ReadonlyArray<ColorMode> = ["light", "dark"];
const EMPTY_ACCENT_COLORS: AccentColors = { light: null, dark: null };
const HEX_COLOR_RE = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i;
const LEGACY_TEAL_INDICATOR = "#ff4438";
const SELECTION_COLOR_SLOTS: ReadonlyArray<SelectionColorSlot> = [
  "selectedBg",
  "hoverBg",
  "hoverInk",
  "selectedBorder",
  "selectedText",
  "indicator",
];
const ACCENT_STYLE_PROPS = [
  "--afs-accent",
  "--afs-accent-dim",
  "--afs-accent-soft",
  "--afs-accent-glow",
  "--afs-accent-fill",
  "--afs-focus",
  "--afs-focus-soft",
  "--afs-focus-ring",
  "--afs-ink-on-accent",
  "--afs-pattern-rgb",
  "--afs-viz-1",
  "--afs-viz-grid",
  "--afs-shadow-glow",
] as const;

export const DARK_TEAL_SELECTION_COLORS: SelectionPalettes = {
  light: {
    selectedBg: "#eaf0f2",
    hoverBg: "#f2f5f6",
    hoverInk: "#163341",
    selectedBorder: "#163341",
    selectedText: "#163341",
    indicator: "#0f766e",
    indicatorWidth: "2px",
  },
  dark: {
    selectedBg: "#102832",
    hoverBg: "#153340",
    hoverInk: "#eaf0f2",
    selectedBorder: "#a8c0c8",
    selectedText: "#eaf0f2",
    indicator: "#5eead4",
    indicatorWidth: "2px",
  },
};

const WARM_STONE_LIGHT_SELECTION: SelectionPalette = {
  selectedBg: "#f4f2ef",
  hoverBg: "#fafaf8",
  hoverInk: "#163341",
  selectedBorder: "#5c707a",
  selectedText: "#163341",
  indicator: "#163341",
  indicatorWidth: "2px",
};

const LEGACY_WARM_STONE_DARK_SELECTION: SelectionPalette = {
  selectedBg: "#242220",
  hoverBg: "#2b2926",
  hoverInk: "#f4f2ef",
  selectedBorder: "#8da0a9",
  selectedText: "#f4f2ef",
  indicator: "#d7d1c9",
  indicatorWidth: "2px",
};

const LEGACY_WARM_STONE_SELECTION_COLORS: SelectionPalettes = {
  light: WARM_STONE_LIGHT_SELECTION,
  dark: LEGACY_WARM_STONE_DARK_SELECTION,
};

export const WARM_STONE_SELECTION_COLORS: SelectionPalettes = {
  light: WARM_STONE_LIGHT_SELECTION,
  dark: DARK_TEAL_SELECTION_COLORS.dark,
};

export const CHARTREUSE_SELECTION_COLORS: SelectionPalettes = {
  light: {
    selectedBg: "#f7f9ec",
    hoverBg: "#fafbf3",
    hoverInk: "#163341",
    selectedBorder: "#163341",
    selectedText: "#163341",
    indicator: "#dcff1e",
    indicatorWidth: "4px",
  },
  dark: {
    selectedBg: "#1d2617",
    hoverBg: "#252f1e",
    hoverInk: "#f7f9ec",
    selectedBorder: "#dfe9d3",
    selectedText: "#f7f9ec",
    indicator: "#dcff1e",
    indicatorWidth: "4px",
  },
};

export const DEFAULT_SELECTION_COLORS: SelectionPalettes =
  CHARTREUSE_SELECTION_COLORS;

function cloneSelectionPalettes(colors: SelectionPalettes): SelectionPalettes {
  return {
    light: { ...colors.light },
    dark: { ...colors.dark },
  };
}

function selectionPalettesEqual(a: SelectionPalettes, b: SelectionPalettes) {
  return VALID_COLOR_MODES.every(
    (mode) =>
      SELECTION_COLOR_SLOTS.every((slot) => a[mode][slot] === b[mode][slot]) &&
      a[mode].indicatorWidth === b[mode].indicatorWidth,
  );
}

function isMigratableDefaultPalette(selectionColors: SelectionPalettes) {
  return (
    selectionPalettesEqual(selectionColors, DARK_TEAL_SELECTION_COLORS) ||
    selectionPalettesEqual(
      selectionColors,
      LEGACY_WARM_STONE_SELECTION_COLORS,
    ) ||
    selectionPalettesEqual(selectionColors, WARM_STONE_SELECTION_COLORS)
  );
}

function isColorMode(value: string | null): value is ColorMode {
  return VALID_COLOR_MODES.includes(value as ColorMode);
}

function readStoredMode(): ColorMode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isColorMode(stored)) return stored;
  } catch {
    // ignore
  }
  return "light";
}

export function normalizeAccentColor(value: string | null): string | null {
  const color = value?.trim();
  if (!color || !HEX_COLOR_RE.test(color)) return null;
  if (color.length === 4) {
    return `#${color[1]}${color[1]}${color[2]}${color[2]}${color[3]}${color[3]}`.toLowerCase();
  }
  return color.toLowerCase();
}

function hexToRgb(hex: string) {
  const value = Number.parseInt(hex.slice(1), 16);
  return {
    r: (value >> 16) & 255,
    g: (value >> 8) & 255,
    b: value & 255,
  };
}

function readableTextForAccent(hex: string) {
  const { r, g, b } = hexToRgb(hex);
  const toLinear = (channel: number) => {
    const value = channel / 255;
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  const luminance =
    0.2126 * toLinear(r) + 0.7152 * toLinear(g) + 0.0722 * toLinear(b);
  return luminance > 0.48 ? "#091a23" : "#ffffff";
}

function readAccentColorsFromValue(value: string | null): AccentColors {
  if (!value) return { ...EMPTY_ACCENT_COLORS };

  try {
    const parsed: unknown = JSON.parse(value);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const stored = parsed as Partial<Record<ColorMode, unknown>>;
      return {
        light:
          typeof stored.light === "string"
            ? normalizeAccentColor(stored.light)
            : null,
        dark:
          typeof stored.dark === "string"
            ? normalizeAccentColor(stored.dark)
            : null,
      };
    }
  } catch {
    const legacy = normalizeAccentColor(value);
    if (legacy) {
      return { light: legacy, dark: legacy };
    }
  }

  return { ...EMPTY_ACCENT_COLORS };
}

function readStoredAccentColors(): AccentColors {
  try {
    return readAccentColorsFromValue(localStorage.getItem(ACCENT_STORAGE_KEY));
  } catch {
    return { ...EMPTY_ACCENT_COLORS };
  }
}

function hasAccentOverride(accentColors: AccentColors) {
  return accentColors.light !== null || accentColors.dark !== null;
}

function normalizeSelectionPalette(
  value: unknown,
  fallback: SelectionPalette,
): SelectionPalette {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return { ...fallback };
  const stored = value as Partial<
    Record<keyof SelectionPalette | "selectedInk", unknown>
  >;
  const next = { ...fallback };

  SELECTION_COLOR_SLOTS.forEach((slot) => {
    if (typeof stored[slot] !== "string") return;
    next[slot] = normalizeAccentColor(stored[slot]) ?? fallback[slot];
  });

  if (typeof stored.selectedText !== "string") {
    const legacySelectedInk =
      typeof stored.selectedInk === "string"
        ? normalizeAccentColor(stored.selectedInk)
        : null;
    next.selectedText = legacySelectedInk ?? next.selectedText;
  }

  if (typeof stored.hoverInk !== "string") {
    next.hoverInk = next.selectedText;
  }

  if (
    typeof stored.indicatorWidth === "string" &&
    stored.indicatorWidth.trim()
  ) {
    next.indicatorWidth = stored.indicatorWidth.trim();
  }

  return next;
}

function migrateLegacyTealIndicator(
  palette: SelectionPalette,
  currentDefault: SelectionPalette,
): SelectionPalette {
  const isLegacyDefault =
    palette.selectedBg === currentDefault.selectedBg &&
    palette.hoverBg === currentDefault.hoverBg &&
    palette.hoverInk === currentDefault.hoverInk &&
    palette.selectedBorder === currentDefault.selectedBorder &&
    palette.selectedText === currentDefault.selectedText &&
    palette.indicator === LEGACY_TEAL_INDICATOR &&
    palette.indicatorWidth === currentDefault.indicatorWidth;

  return isLegacyDefault
    ? { ...palette, indicator: currentDefault.indicator }
    : palette;
}

function readSelectionColorsFromValue(value: string | null): SelectionPalettes {
  if (!value)
    return {
      light: { ...DEFAULT_SELECTION_COLORS.light },
      dark: { ...DEFAULT_SELECTION_COLORS.dark },
    };

  try {
    const parsed: unknown = JSON.parse(value);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const stored = parsed as Partial<Record<ColorMode, unknown>>;
      const light = normalizeSelectionPalette(
        stored.light,
        DEFAULT_SELECTION_COLORS.light,
      );
      const dark = normalizeSelectionPalette(
        stored.dark,
        DEFAULT_SELECTION_COLORS.dark,
      );
      return {
        light: migrateLegacyTealIndicator(
          light,
          DARK_TEAL_SELECTION_COLORS.light,
        ),
        dark: migrateLegacyTealIndicator(dark, DARK_TEAL_SELECTION_COLORS.dark),
      };
    }
  } catch {
    // ignore
  }

  return {
    light: { ...DEFAULT_SELECTION_COLORS.light },
    dark: { ...DEFAULT_SELECTION_COLORS.dark },
  };
}

function readStoredSelectionColors(): SelectionPalettes {
  try {
    const storedVersion = localStorage.getItem(SELECTION_VERSION_STORAGE_KEY);
    const selectionColors = readSelectionColorsFromValue(
      localStorage.getItem(SELECTION_STORAGE_KEY),
    );
    if (
      storedVersion !== CURRENT_SELECTION_VERSION &&
      isMigratableDefaultPalette(selectionColors)
    ) {
      return cloneSelectionPalettes(DEFAULT_SELECTION_COLORS);
    }
    return selectionColors;
  } catch {
    return cloneSelectionPalettes(DEFAULT_SELECTION_COLORS);
  }
}

export function ColorModeProvider({
  children,
}: {
  children: (colorMode: ColorMode) => ReactNode;
}) {
  const [colorMode, setColorMode] = useState<ColorMode>(readStoredMode);
  const [accentColors, setAccentColors] = useState<AccentColors>(
    readStoredAccentColors,
  );
  const [selectionColors, setSelectionColors] = useState<SelectionPalettes>(
    readStoredSelectionColors,
  );
  const accentColor = accentColors[colorMode];
  const selectionPalette = selectionColors[colorMode];

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, colorMode);
    } catch {
      // ignore
    }
    document.documentElement.setAttribute("data-theme", colorMode);
  }, [colorMode]);

  useEffect(() => {
    const rootStyle = document.documentElement.style;

    if (!accentColor) {
      ACCENT_STYLE_PROPS.forEach((prop) => rootStyle.removeProperty(prop));
      return;
    }

    const { r, g, b } = hexToRgb(accentColor);
    rootStyle.setProperty("--afs-accent", accentColor);
    rootStyle.setProperty("--afs-accent-fill", accentColor);
    rootStyle.setProperty(
      "--afs-accent-dim",
      `color-mix(in srgb, ${accentColor} 78%, black)`,
    );
    rootStyle.setProperty("--afs-accent-soft", `rgba(${r}, ${g}, ${b}, 0.12)`);
    rootStyle.setProperty("--afs-accent-glow", `rgba(${r}, ${g}, ${b}, 0.28)`);
    rootStyle.setProperty("--afs-focus", accentColor);
    rootStyle.setProperty("--afs-focus-soft", `rgba(${r}, ${g}, ${b}, 0.18)`);
    rootStyle.setProperty(
      "--afs-focus-ring",
      `0 0 0 2px var(--afs-bg), 0 0 0 4px ${accentColor}`,
    );
    rootStyle.setProperty(
      "--afs-ink-on-accent",
      readableTextForAccent(accentColor),
    );
    rootStyle.setProperty("--afs-pattern-rgb", `${r}, ${g}, ${b}`);
    rootStyle.setProperty("--afs-viz-1", accentColor);
    rootStyle.setProperty("--afs-viz-grid", `rgba(${r}, ${g}, ${b}, 0.04)`);
    rootStyle.setProperty(
      "--afs-shadow-glow",
      "0 0 12px var(--afs-accent-glow)",
    );
  }, [accentColor, colorMode]);

  useEffect(() => {
    const rootStyle = document.documentElement.style;

    rootStyle.setProperty("--afs-selection-bg", selectionPalette.selectedBg);
    rootStyle.setProperty("--afs-selection-hover-bg", selectionPalette.hoverBg);
    rootStyle.setProperty(
      "--afs-selection-hover-ink",
      selectionPalette.hoverInk,
    );
    rootStyle.setProperty(
      "--afs-selection-border",
      selectionPalette.selectedBorder,
    );
    rootStyle.setProperty(
      "--afs-selection-text",
      selectionPalette.selectedText,
    );
    rootStyle.setProperty("--afs-selection-ink", selectionPalette.selectedText);
    rootStyle.setProperty(
      "--afs-selection-indicator",
      selectionPalette.indicator,
    );
    rootStyle.setProperty(
      "--afs-selection-indicator-width",
      selectionPalette.indicatorWidth,
    );
  }, [selectionPalette]);

  useEffect(() => {
    try {
      if (hasAccentOverride(accentColors)) {
        localStorage.setItem(ACCENT_STORAGE_KEY, JSON.stringify(accentColors));
      } else {
        localStorage.removeItem(ACCENT_STORAGE_KEY);
      }
    } catch {
      // ignore
    }
  }, [accentColors]);

  useEffect(() => {
    try {
      localStorage.setItem(
        SELECTION_STORAGE_KEY,
        JSON.stringify(selectionColors),
      );
      localStorage.setItem(
        SELECTION_VERSION_STORAGE_KEY,
        CURRENT_SELECTION_VERSION,
      );
    } catch {
      // ignore
    }
  }, [selectionColors]);

  useEffect(() => {
    function handleStorage(event: StorageEvent) {
      if (event.key === STORAGE_KEY && isColorMode(event.newValue)) {
        setColorMode(event.newValue);
        return;
      }

      if (event.key === ACCENT_STORAGE_KEY) {
        setAccentColors(readAccentColorsFromValue(event.newValue));
        return;
      }

      if (event.key === SELECTION_STORAGE_KEY) {
        setSelectionColors(readSelectionColorsFromValue(event.newValue));
      }
    }

    window.addEventListener("storage", handleStorage);
    return () => window.removeEventListener("storage", handleStorage);
  }, []);

  const toggleColorMode = useCallback(() => {
    setColorMode((prev) => (prev === "light" ? "dark" : "light"));
  }, []);

  const setAccentColor = useCallback((targetMode: ColorMode, next: string) => {
    const normalized = normalizeAccentColor(next);
    if (!normalized) return;
    setAccentColors((prev) => ({ ...prev, [targetMode]: normalized }));
  }, []);

  const resetAccentColor = useCallback((targetMode: ColorMode) => {
    setAccentColors((prev) => ({ ...prev, [targetMode]: null }));
  }, []);

  const setSelectionColor = useCallback(
    (targetMode: ColorMode, slot: SelectionColorSlot, next: string) => {
      const normalized = normalizeAccentColor(next);
      if (!normalized) return;
      setSelectionColors((prev) => ({
        ...prev,
        [targetMode]: {
          ...prev[targetMode],
          [slot]: normalized,
        },
      }));
    },
    [],
  );

  const setSelectionPalette = useCallback((next: SelectionPalettes) => {
    setSelectionColors({
      light: { ...next.light },
      dark: { ...next.dark },
    });
  }, []);

  const value = useMemo<ThemeContextValue>(
    () => ({
      colorMode,
      setColorMode,
      toggleColorMode,
      accentColor,
      accentColors,
      setAccentColor,
      resetAccentColor,
      selectionColors,
      setSelectionColor,
      setSelectionPalette,
    }),
    [
      accentColor,
      accentColors,
      colorMode,
      resetAccentColor,
      selectionColors,
      setAccentColor,
      setSelectionColor,
      setSelectionPalette,
      toggleColorMode,
    ],
  );

  return (
    <ThemeContext.Provider value={value}>
      {children(colorMode)}
    </ThemeContext.Provider>
  );
}

export function useColorMode() {
  const ctx = useContext(ThemeContext);
  if (!ctx)
    throw new Error("useColorMode must be used inside ColorModeProvider");
  return ctx;
}
