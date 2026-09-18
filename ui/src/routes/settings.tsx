import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import styled from "styled-components";
import {
  PageStack,
  SectionCard,
  SectionHeader,
  SectionTitle,
} from "../components/afs-kit";
import type { Skin } from "../foundation/skin-context";
import { useSkin } from "../foundation/skin-context";
import type {
  ColorMode,
  SelectionColorSlot,
  SelectionPalette,
  SelectionPalettes,
} from "../foundation/theme-context";
import {
  CHARTREUSE_SELECTION_COLORS,
  DARK_TEAL_SELECTION_COLORS,
  DEFAULT_SELECTION_COLORS,
  normalizeAccentColor,
  useColorMode,
  WARM_STONE_SELECTION_COLORS,
} from "../foundation/theme-context";

export const Route = createFileRoute("/settings")({
  component: () => (
    <PageStack>
      <SkinSwitcher />
    </PageStack>
  ),
});
type SelectionPreviewState = "idle" | "hover" | "selected";
const SKIN_OPTIONS: ReadonlyArray<{
  value: Skin;
  label: string;
  body: string;
}> = [
  {
    value: "classic",
    label: "Classic",
    body: "Today's Redis-UI styling with light surfaces and rounded cards.",
  },
  {
    value: "situation-room",
    label: "Modern",
    body: "Mono-first canvas with a focused app shell, crisp spacing, and high-contrast accents. Work in progress.",
  },
];

const ACCENT_MODE_OPTIONS: ReadonlyArray<{ value: ColorMode; label: string }> =
  [
    { value: "light", label: "Light theme" },
    { value: "dark", label: "Dark theme" },
  ];

const SELECTION_COLOR_FIELDS: ReadonlyArray<{
  slot: SelectionColorSlot;
  label: string;
}> = [
  { slot: "selectedBg", label: "Selected bg" },
  { slot: "selectedText", label: "Selected text" },
  { slot: "hoverBg", label: "Hover bg" },
  { slot: "hoverInk", label: "Hover text" },
  { slot: "selectedBorder", label: "Border" },
  { slot: "indicator", label: "Indicator" },
];

const SELECTION_PRESETS: ReadonlyArray<{
  id: string;
  label: string;
  title: string;
  colors: SelectionPalettes;
}> = [
  {
    id: "dark-teal",
    label: "A",
    title: "Dark teal tint",
    colors: DARK_TEAL_SELECTION_COLORS,
  },
  {
    id: "warm-stone",
    label: "B",
    title: "Warm stone",
    colors: WARM_STONE_SELECTION_COLORS,
  },
  {
    id: "chartreuse",
    label: "C",
    title: "Chartreuse neutral",
    colors: CHARTREUSE_SELECTION_COLORS,
  },
  {
    id: "brick",
    label: "D",
    title: "Desaturated brick",
    colors: {
      light: {
        selectedBg: "#f7efee",
        hoverBg: "#fbf7f6",
        hoverInk: "#7a0c00",
        selectedBorder: "#7a0c00",
        selectedText: "#7a0c00",
        indicator: "#7a0c00",
        indicatorWidth: "1px",
      },
      dark: {
        selectedBg: "#2a1715",
        hoverBg: "#341d1a",
        hoverInk: "#ffe9e6",
        selectedBorder: "#ff8a7d",
        selectedText: "#ffe9e6",
        indicator: "#ff8a7d",
        indicatorWidth: "1px",
      },
    },
  },
  {
    id: "monochrome",
    label: "E",
    title: "Black and white",
    colors: {
      light: {
        selectedBg: "#f2f2f2",
        hoverBg: "#fafafa",
        hoverInk: "#111111",
        selectedBorder: "#111111",
        selectedText: "#111111",
        indicator: "#111111",
        indicatorWidth: "2px",
      },
      dark: {
        selectedBg: "#171717",
        hoverBg: "#242424",
        hoverInk: "#f5f5f5",
        selectedBorder: "#f5f5f5",
        selectedText: "#f5f5f5",
        indicator: "#f5f5f5",
        indicatorWidth: "2px",
      },
    },
  },
];

function SkinSwitcher() {
  const { skin, setSkin, isSwitcherEnabled } = useSkin();
  const {
    colorMode,
    selectionColors,
    setColorMode,
    setSelectionColor,
    setSelectionPalette,
  } = useColorMode();
  const [selectionDrafts, setSelectionDrafts] = useState<
    Record<ColorMode, Record<SelectionColorSlot, string>>
  >({
    light: createSelectionDrafts(DEFAULT_SELECTION_COLORS.light),
    dark: createSelectionDrafts(DEFAULT_SELECTION_COLORS.dark),
  });
  const [isAppearanceOpen, setAppearanceOpen] = useState(false);

  useEffect(() => {
    setSelectionDrafts({
      light: createSelectionDrafts(selectionColors.light),
      dark: createSelectionDrafts(selectionColors.dark),
    });
  }, [selectionColors]);

  if (!isSwitcherEnabled) return null;

  function chooseSelectionColor(
    targetMode: ColorMode,
    slot: SelectionColorSlot,
    next: string,
  ) {
    const normalized = normalizeAccentColor(next);
    if (!normalized) return;
    setSelectionDrafts((prev) => ({
      ...prev,
      [targetMode]: {
        ...prev[targetMode],
        [slot]: normalized.toUpperCase(),
      },
    }));
    setSelectionColor(targetMode, slot, normalized);
  }

  const activeSelectionPreset = SELECTION_PRESETS.find((preset) =>
    selectionPalettesEqual(preset.colors, selectionColors),
  );
  const activeSkinOption = SKIN_OPTIONS.find((option) => option.value === skin);

  return (
    <SectionCard $span={12}>
      <SectionHeader>
        <SectionTitle
          title="Appearance"
          body="Choose the console skin and selection palette. These settings are stored locally in your browser."
        />
      </SectionHeader>
      <AppearanceControlRow>
        <AppearanceSummary>
          <AppearanceSummaryLabel>
            {activeSkinOption?.label ?? "Custom"} skin
          </AppearanceSummaryLabel>
          <AppearanceSummaryMeta>
            {activeSelectionPreset
              ? `Option ${activeSelectionPreset.label}: ${activeSelectionPreset.title}`
              : "Custom selection palette"}
          </AppearanceSummaryMeta>
        </AppearanceSummary>
        <AppearanceToggleButton
          type="button"
          aria-expanded={isAppearanceOpen}
          onClick={() => setAppearanceOpen((value) => !value)}
        >
          {isAppearanceOpen ? "Hide theme settings" : "Customize theme"}
        </AppearanceToggleButton>
      </AppearanceControlRow>
      {isAppearanceOpen ? (
        <AppearanceControls>
          <SkinGrid>
            {SKIN_OPTIONS.map((option) => {
              const active = skin === option.value;
              return (
                <SkinOption
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  $active={active}
                  onClick={() => setSkin(option.value)}
                >
                  <SkinOptionLabel>{option.label}</SkinOptionLabel>
                  <SkinOptionBody>{option.body}</SkinOptionBody>
                  {active ? <SkinOptionStatus>Active</SkinOptionStatus> : null}
                </SkinOption>
              );
            })}
          </SkinGrid>
          <SelectionPaletteArea>
            <SelectionPaletteHeader>
              <SelectionPaletteTitle>Selection palette</SelectionPaletteTitle>
              <SelectionPaletteMeta>
                {activeSelectionPreset
                  ? `Option ${activeSelectionPreset.label}: ${activeSelectionPreset.title}`
                  : "Custom"}
              </SelectionPaletteMeta>
            </SelectionPaletteHeader>
            <SelectionPresetGrid>
              {SELECTION_PRESETS.map((preset) => {
                const active = selectionPalettesEqual(
                  preset.colors,
                  selectionColors,
                );
                return (
                  <SelectionPresetButton
                    key={preset.id}
                    type="button"
                    $active={active}
                    onClick={() => setSelectionPalette(preset.colors)}
                  >
                    <SelectionPresetName>
                      <SelectionPresetLetter>
                        {preset.label}
                      </SelectionPresetLetter>
                      <span>{preset.title}</span>
                    </SelectionPresetName>
                    <SelectionPresetSwatches>
                      <SelectionPresetSwatch
                        aria-label={`${preset.title} light preview`}
                        $palette={preset.colors.light}
                      />
                      <SelectionPresetSwatch
                        aria-label={`${preset.title} dark preview`}
                        $palette={preset.colors.dark}
                      />
                    </SelectionPresetSwatches>
                  </SelectionPresetButton>
                );
              })}
            </SelectionPresetGrid>
            <SelectionPreviewGrid aria-label="Selection states preview">
              {ACCENT_MODE_OPTIONS.map((option) => {
                const palette = selectionColors[option.value];
                return (
                  <SelectionPreviewPanel
                    key={option.value}
                    $mode={option.value}
                  >
                    <AccentThemeHeader>
                      <AccentPickerCopy>
                        <AccentPickerTitle>{option.label}</AccentPickerTitle>
                        <AccentPickerMeta>
                          Idle / hover / selected
                        </AccentPickerMeta>
                      </AccentPickerCopy>
                    </AccentThemeHeader>
                    <SelectionPreviewRows>
                      <SelectionPreviewRow
                        $mode={option.value}
                        $palette={palette}
                        $state="idle"
                      >
                        <SelectionPreviewIcon aria-hidden />
                        <SelectionPreviewCopy>
                          <SelectionPreviewTitle>
                            Idle item
                          </SelectionPreviewTitle>
                          <SelectionPreviewMeta>
                            Base row text
                          </SelectionPreviewMeta>
                        </SelectionPreviewCopy>
                      </SelectionPreviewRow>
                      <SelectionPreviewRow
                        $mode={option.value}
                        $palette={palette}
                        $state="hover"
                      >
                        <SelectionPreviewIcon aria-hidden />
                        <SelectionPreviewCopy>
                          <SelectionPreviewTitle>
                            Hover item
                          </SelectionPreviewTitle>
                          <SelectionPreviewMeta>
                            Uses hover bg + ink
                          </SelectionPreviewMeta>
                        </SelectionPreviewCopy>
                      </SelectionPreviewRow>
                      <SelectionPreviewRow
                        $mode={option.value}
                        $palette={palette}
                        $state="selected"
                      >
                        <SelectionPreviewIcon aria-hidden />
                        <SelectionPreviewCopy>
                          <SelectionPreviewTitle>
                            Selected item
                          </SelectionPreviewTitle>
                          <SelectionPreviewMeta>
                            Border, text, indicator
                          </SelectionPreviewMeta>
                        </SelectionPreviewCopy>
                      </SelectionPreviewRow>
                    </SelectionPreviewRows>
                  </SelectionPreviewPanel>
                );
              })}
            </SelectionPreviewGrid>
            <SelectionEditorGrid>
              {ACCENT_MODE_OPTIONS.map((option) => (
                <SelectionEditorCard key={option.value}>
                  <AccentThemeHeader>
                    <AccentPickerCopy>
                      <AccentPickerTitle>{option.label}</AccentPickerTitle>
                      <AccentPickerMeta>Selection colors</AccentPickerMeta>
                    </AccentPickerCopy>
                    {colorMode === option.value ? (
                      <AccentThemeStatus>Viewing</AccentThemeStatus>
                    ) : (
                      <AccentViewButton
                        type="button"
                        onClick={() => setColorMode(option.value)}
                      >
                        View
                      </AccentViewButton>
                    )}
                  </AccentThemeHeader>
                  <SelectionColorGrid>
                    {SELECTION_COLOR_FIELDS.map((field) => {
                      const color = selectionColors[option.value][field.slot];
                      return (
                        <SelectionField key={field.slot}>
                          <SelectionFieldLabel>
                            {field.label}
                          </SelectionFieldLabel>
                          <SelectionColorControls>
                            <AccentPickerInputWrap $color={color}>
                              <AccentPickerInput
                                aria-label={`${option.label} ${field.label} color picker`}
                                type="color"
                                value={color}
                                onChange={(event) =>
                                  chooseSelectionColor(
                                    option.value,
                                    field.slot,
                                    event.currentTarget.value,
                                  )
                                }
                              />
                            </AccentPickerInputWrap>
                            <SelectionHexInput
                              aria-label={`${option.label} ${field.label} hex color`}
                              value={selectionDrafts[option.value][field.slot]}
                              spellCheck={false}
                              onBlur={() => {
                                if (
                                  !normalizeAccentColor(
                                    selectionDrafts[option.value][field.slot],
                                  )
                                ) {
                                  setSelectionDrafts((prev) => ({
                                    ...prev,
                                    [option.value]: {
                                      ...prev[option.value],
                                      [field.slot]: color.toUpperCase(),
                                    },
                                  }));
                                }
                              }}
                              onChange={(event) => {
                                const next = event.currentTarget.value;
                                setSelectionDrafts((prev) => ({
                                  ...prev,
                                  [option.value]: {
                                    ...prev[option.value],
                                    [field.slot]: next.toUpperCase(),
                                  },
                                }));
                                const normalized = normalizeAccentColor(next);
                                if (normalized) {
                                  setSelectionColor(
                                    option.value,
                                    field.slot,
                                    normalized,
                                  );
                                }
                              }}
                            />
                          </SelectionColorControls>
                        </SelectionField>
                      );
                    })}
                  </SelectionColorGrid>
                </SelectionEditorCard>
              ))}
            </SelectionEditorGrid>
          </SelectionPaletteArea>
        </AppearanceControls>
      ) : null}
    </SectionCard>
  );
}

function createSelectionDrafts(selectionPalette: SelectionPalettes[ColorMode]) {
  return {
    selectedBg: selectionPalette.selectedBg.toUpperCase(),
    hoverBg: selectionPalette.hoverBg.toUpperCase(),
    hoverInk: selectionPalette.hoverInk.toUpperCase(),
    selectedBorder: selectionPalette.selectedBorder.toUpperCase(),
    selectedText: selectionPalette.selectedText.toUpperCase(),
    indicator: selectionPalette.indicator.toUpperCase(),
  };
}

function selectionPalettesEqual(a: SelectionPalettes, b: SelectionPalettes) {
  return ACCENT_MODE_OPTIONS.every(
    (mode) =>
      SELECTION_COLOR_FIELDS.every(
        (field) => a[mode.value][field.slot] === b[mode.value][field.slot],
      ) && a[mode.value].indicatorWidth === b[mode.value].indicatorWidth,
  );
}

const AppearanceControlRow = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  border: 1px solid var(--afs-line);
  border-radius: 10px;
  background: var(--afs-panel-strong);
  padding: 12px 14px;

  @media (max-width: 640px) {
    align-items: stretch;
    flex-direction: column;
  }
`;

const AppearanceSummary = styled.div`
  min-width: 0;
  display: grid;
  gap: 3px;
`;

const AppearanceSummaryLabel = styled.span`
  color: var(--afs-ink);
  font-size: 13px;
  font-weight: 800;
`;

const AppearanceSummaryMeta = styled.span`
  color: var(--afs-muted);
  font-family: var(--afs-mono);
  font-size: 12px;
`;

const AppearanceToggleButton = styled.button`
  flex: 0 0 auto;
  min-height: 34px;
  border: 1px solid var(--afs-selection-border);
  border-radius: 8px;
  background: var(--afs-selection-bg);
  color: var(--afs-selection-text);
  padding: 0 13px;
  font: inherit;
  font-size: 12px;
  font-weight: 800;
  cursor: pointer;
  box-shadow: inset var(--afs-selection-indicator-width) 0 0
    var(--afs-selection-indicator);
  transition:
    background 0.15s ease,
    color 0.15s ease,
    border-color 0.15s ease;

  &:hover {
    background: var(--afs-selection-hover-bg);
    color: var(--afs-selection-hover-ink);
  }

  &:focus-visible {
    outline: 2px solid var(--afs-focus);
    outline-offset: 2px;
  }
`;

const AppearanceControls = styled.div`
  display: grid;
  gap: 18px;
  margin-top: 18px;
`;

const SkinGrid = styled.div`
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(2, minmax(0, 1fr));

  @media (max-width: 760px) {
    grid-template-columns: 1fr;
  }
`;

const AccentThemeHeader = styled.div`
  min-width: 0;
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
`;

const AccentPickerCopy = styled.span`
  display: grid;
  gap: 2px;
  min-width: 0;
`;

const AccentPickerTitle = styled.span`
  color: var(--afs-ink);
  font-size: 13px;
  font-weight: 700;
`;

const AccentPickerMeta = styled.span`
  color: var(--afs-muted);
  font-family: var(--afs-mono);
  font-size: 12px;
`;

const AccentPickerInputWrap = styled.span<{ $color: string }>`
  position: relative;
  display: inline-grid;
  width: 42px;
  height: 42px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid
    color-mix(in srgb, ${({ $color }) => $color} 46%, var(--afs-line));
  border-radius: 10px;
  background:
    linear-gradient(45deg, rgba(127, 127, 127, 0.18) 25%, transparent 25%),
    linear-gradient(-45deg, rgba(127, 127, 127, 0.18) 25%, transparent 25%),
    linear-gradient(45deg, transparent 75%, rgba(127, 127, 127, 0.18) 75%),
    linear-gradient(-45deg, transparent 75%, rgba(127, 127, 127, 0.18) 75%);
  background-position:
    0 0,
    0 7px,
    7px -7px,
    -7px 0;
  background-size: 14px 14px;
  overflow: hidden;

  &::after {
    content: "";
    position: absolute;
    inset: 5px;
    border-radius: 6px;
    background: ${({ $color }) => $color};
    box-shadow: inset 0 0 0 1px rgba(0, 0, 0, 0.12);
    pointer-events: none;
  }
`;

const AccentPickerInput = styled.input`
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  border: 0;
  padding: 0;
  opacity: 0;
  cursor: pointer;
`;

const AccentHexInput = styled.input`
  width: 108px;
  min-height: 34px;
  border: 1px solid var(--afs-line);
  border-radius: 8px;
  background: var(--afs-panel-strong);
  color: var(--afs-ink);
  padding: 0 10px;
  font: inherit;
  font-family: var(--afs-mono);
  font-size: 12px;
  text-transform: uppercase;

  &:focus {
    border-color: var(--afs-accent);
    box-shadow: 0 0 0 2px var(--afs-accent-soft);
    outline: none;
  }
`;

const AccentThemeStatus = styled.span`
  flex: 0 0 auto;
  min-height: 26px;
  display: inline-flex;
  align-items: center;
  border: 1px solid var(--afs-selection-border);
  border-radius: 999px;
  background: var(--afs-selection-bg);
  padding: 0 10px;
  color: var(--afs-selection-text);
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
`;

const AccentViewButton = styled.button`
  flex: 0 0 auto;
  min-height: 26px;
  border: 1px solid var(--afs-line);
  border-radius: 999px;
  background: var(--afs-panel);
  color: var(--afs-ink);
  padding: 0 10px;
  font: inherit;
  font-size: 11px;
  font-weight: 800;
  cursor: pointer;

  &:hover {
    border-color: var(--afs-selection-border);
    background: var(--afs-selection-hover-bg);
    color: var(--afs-selection-hover-ink);
  }

  &:focus-visible {
    outline: 2px solid var(--afs-focus);
    outline-offset: 2px;
  }
`;

const SelectionPaletteArea = styled.div`
  border-top: 1px solid var(--afs-line);
  padding-top: 18px;
  display: grid;
  gap: 14px;
`;

const SelectionPaletteHeader = styled.div`
  min-width: 0;
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;

  @media (max-width: 760px) {
    align-items: flex-start;
    flex-direction: column;
  }
`;

const SelectionPaletteTitle = styled.h3`
  margin: 0;
  color: var(--afs-ink);
  font-size: 14px;
  font-weight: 800;
`;

const SelectionPaletteMeta = styled.span`
  color: var(--afs-muted);
  font-family: var(--afs-mono);
  font-size: 12px;
`;

const SelectionPresetGrid = styled.div`
  display: grid;
  gap: 10px;
  grid-template-columns: repeat(5, minmax(0, 1fr));

  @media (max-width: 1180px) {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  @media (max-width: 760px) {
    grid-template-columns: 1fr;
  }
`;

const SelectionPresetButton = styled.button<{ $active: boolean }>`
  min-width: 0;
  border: 1px solid
    ${({ $active }) =>
      $active ? "var(--afs-selection-border)" : "var(--afs-line)"};
  border-radius: 10px;
  background: ${({ $active }) =>
    $active ? "var(--afs-selection-bg)" : "var(--afs-panel-strong)"};
  color: ${({ $active }) =>
    $active ? "var(--afs-selection-text)" : "var(--afs-ink)"};
  padding: 12px;
  display: grid;
  gap: 10px;
  text-align: left;
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background 0.15s ease,
    color 0.15s ease;

  &:hover {
    border-color: var(--afs-selection-border);
    background: var(--afs-selection-hover-bg);
    color: ${({ $active }) =>
      $active ? "var(--afs-selection-text)" : "var(--afs-selection-hover-ink)"};
  }

  &:focus-visible {
    outline: 2px solid var(--afs-focus);
    outline-offset: 2px;
  }
`;

const SelectionPresetName = styled.span`
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  color: inherit;
  font-size: 12px;
  font-weight: 800;
`;

const SelectionPresetLetter = styled.span`
  width: 22px;
  height: 22px;
  flex: 0 0 auto;
  display: inline-grid;
  place-items: center;
  border: 1px solid currentColor;
  border-radius: 999px;
  font-size: 11px;
`;

const SelectionPresetSwatches = styled.span`
  display: grid;
  gap: 6px;
`;

const SelectionPresetSwatch = styled.span<{
  $palette: SelectionPalettes[ColorMode];
}>`
  position: relative;
  min-height: 30px;
  border: 1px solid ${({ $palette }) => $palette.selectedBorder};
  border-radius: 7px;
  background: ${({ $palette }) => $palette.selectedBg};
  color: ${({ $palette }) => $palette.selectedText};
  overflow: hidden;

  &::before {
    content: "";
    position: absolute;
    inset: 0 auto 0 0;
    width: ${({ $palette }) => $palette.indicatorWidth};
    background: ${({ $palette }) => $palette.indicator};
  }

  &::after {
    content: "";
    position: absolute;
    left: 12px;
    right: 12px;
    top: 50%;
    height: 7px;
    transform: translateY(-50%);
    border-radius: 999px;
    background: currentColor;
    opacity: 0.34;
  }
`;

const SelectionPreviewGrid = styled.div`
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(2, minmax(0, 1fr));

  @media (max-width: 980px) {
    grid-template-columns: 1fr;
  }
`;

const SelectionPreviewPanel = styled.div<{ $mode: ColorMode }>`
  min-width: 0;
  border: 1px solid ${({ $mode }) => ($mode === "dark" ? "#284a5e" : "#d1d3d4")};
  border-radius: 10px;
  background: ${({ $mode }) => ($mode === "dark" ? "#091a23" : "#f8f8f8")};
  color: ${({ $mode }) => ($mode === "dark" ? "#e8e6df" : "#282828")};
  padding: 14px;
  display: grid;
  gap: 12px;
`;

const SelectionPreviewRows = styled.div`
  display: grid;
  gap: 8px;
`;

const SelectionPreviewRow = styled.div<{
  $mode: ColorMode;
  $palette: SelectionPalette;
  $state: SelectionPreviewState;
}>`
  min-width: 0;
  min-height: 44px;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  border-radius: 8px;
  padding: 8px 12px;
  border: 1px solid transparent;
  transition: none;
  ${({ $mode, $palette, $state }) => {
    const baseBackground = $mode === "dark" ? "#0e2330" : "#ffffff";
    const baseText = $mode === "dark" ? "#b8b6ae" : "#414042";

    if ($state === "selected") {
      return `
        border-color: ${$palette.selectedBorder};
        background: ${$palette.selectedBg};
        color: ${$palette.selectedText};
        box-shadow:
          inset 0 0 0 1px ${$palette.selectedBorder},
          inset ${$palette.indicatorWidth} 0 0 ${$palette.indicator};
      `;
    }

    if ($state === "hover") {
      return `
        border-color: color-mix(in srgb, ${$palette.selectedBorder} 40%, transparent);
        background: ${$palette.hoverBg};
        color: ${$palette.hoverInk};
      `;
    }

    return `
      border-color: ${$mode === "dark" ? "#1d3645" : "#e6e6e6"};
      background: ${baseBackground};
      color: ${baseText};
    `;
  }}
`;

const SelectionPreviewIcon = styled.span`
  width: 22px;
  height: 22px;
  display: inline-grid;
  place-items: center;
  color: currentColor;

  &::before {
    content: "";
    width: 14px;
    height: 14px;
    border: 2px solid currentColor;
    border-radius: 4px;
    opacity: 0.76;
  }
`;

const SelectionPreviewCopy = styled.span`
  min-width: 0;
  display: grid;
  gap: 1px;
`;

const SelectionPreviewTitle = styled.span`
  min-width: 0;
  font-size: 13px;
  font-weight: 800;
  color: currentColor;
`;

const SelectionPreviewMeta = styled.span`
  min-width: 0;
  font-family: var(--afs-mono);
  font-size: 11px;
  color: currentColor;
  opacity: 0.68;
`;

const SelectionEditorGrid = styled.div`
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(2, minmax(0, 1fr));

  @media (max-width: 980px) {
    grid-template-columns: 1fr;
  }
`;

const SelectionEditorCard = styled.div`
  min-width: 0;
  border: 1px solid var(--afs-line);
  border-radius: 10px;
  background: var(--afs-panel-strong);
  padding: 14px;
  display: grid;
  gap: 12px;
`;

const SelectionColorGrid = styled.div`
  display: grid;
  gap: 10px;
  grid-template-columns: repeat(2, minmax(0, 1fr));

  @media (max-width: 640px) {
    grid-template-columns: 1fr;
  }
`;

const SelectionField = styled.label`
  min-width: 0;
  display: grid;
  gap: 6px;
`;

const SelectionFieldLabel = styled.span`
  color: var(--afs-muted);
  font-size: 11px;
  font-weight: 800;
`;

const SelectionColorControls = styled.span`
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
`;

const SelectionHexInput = styled(AccentHexInput)`
  width: min(100%, 112px);
`;

const SkinOption = styled.button<{ $active: boolean }>`
  position: relative;
  border: 1px solid
    ${({ $active }) =>
      $active ? "var(--afs-selection-border)" : "var(--afs-line)"};
  background: ${({ $active }) =>
    $active ? "var(--afs-selection-bg)" : "var(--afs-panel-strong)"};
  border-radius: 12px;
  padding: 16px 18px;
  text-align: left;
  cursor: pointer;
  display: grid;
  gap: 6px;
  font: inherit;
  color: ${({ $active }) =>
    $active ? "var(--afs-selection-text)" : "var(--afs-ink)"};
  transition:
    border-color 0.15s ease,
    background 0.15s ease;

  &:hover {
    border-color: ${({ $active }) =>
      $active ? "var(--afs-selection-border)" : "var(--afs-line-strong)"};
    background: ${({ $active }) =>
      $active ? "var(--afs-selection-bg)" : "var(--afs-selection-hover-bg)"};
    color: ${({ $active }) =>
      $active ? "var(--afs-selection-text)" : "var(--afs-selection-hover-ink)"};
  }

  &:focus-visible {
    outline: 2px solid var(--afs-focus);
    outline-offset: 2px;
  }
`;

const SkinOptionLabel = styled.span`
  font-size: 14px;
  font-weight: 700;
  color: inherit;
`;

const SkinOptionBody = styled.span`
  font-size: 13px;
  color: var(--afs-muted);
  line-height: 1.5;
`;

const SkinOptionStatus = styled.span`
  position: absolute;
  top: 12px;
  right: 14px;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--afs-selection-indicator);
`;
