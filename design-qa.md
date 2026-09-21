# AFS Home design review

final result: passed

## Reference and evidence

- Source visual: the TypeSafe Home screenshot attached to the user request in this task (1229 × 768 pixels, including Safari chrome). No local source file was supplied. This is a layout inspiration, not a pixel-for-pixel clone.
- Implementation: http://127.0.0.1:8097/ — this checkout's embedded UI, backed only by disposable Redis.
- Final full composition: `/private/tmp/afs-home-final.png`, captured with a 1230 × 1271 CSS viewport to inspect all sections without stitching. Browser image is 1225 × 1266 pixels; capture chrome accounts for the small size difference. No image resizing or artwork edits were performed for QA.
- Reference-width view: `/private/tmp/afs-home-preview.png`, 1230 × 820 CSS viewport, 1225 × 817 capture.
- Narrow Quickstart: `/private/tmp/afs-home-mobile-quickstart.png`. A 390 × 844 breakpoint was also inspected; main content was 330px wide with scrollWidth equal to clientWidth (no horizontal overflow).
- Dark view: `/private/tmp/afs-home-dark.png`; inspected at 1230 × 820. The browser's full-page screenshot has a scaling artifact, so the normal viewport capture and live inspection were used for judgment.
- Final state: light theme, Home, agent-prompt mode. Temporary viewport override reset after inspection; preview left open.

## Comparison

The supplied visual and rendered captures were reviewed in the task's visual context. Both use a left navigation rail, an editorial learning banner, practical recipes on the left, and a Quickstart/resource column on the right. AFS intentionally retains its current shell, theme tokens, grid background and icon family. Its four recipes are actionable drawers rather than external links. The reference's demo and usage blocks were replaced with workspace and Monitor entry points, without invented usage data.

- Typography: existing Geist sans for content, existing monospace for labels and prompts. Clear hero/section/card hierarchy, no title clipping at desktop or narrow widths. Small technical labels match the reference's secondary role.
- Spacing/layout: responsive two-column body with bordered cookbook grid and a separated Quickstart column. Narrow layouts stack in DOM order. Banner tightened during review to bring setup controls higher. Full-page composition and focused Quickstart checks show no overlaps.
- Colors/tokens: AFS light and dark tokens retained. Off-white learning illustration is deliberately a light feature in both modes. Links, selections and focus outlines use the current theme accent.
- Images: custom raster illustration of persistent files shared between agent terminals, complete objects with no clipped edges. Existing Lucide library supplies UI icons. Asset: `ui/public/images/afs-home-workspace.png`, generated using the built-in imagegen tool. Final generation prompt: `docs/evidence/afs-home-hero-prompt.txt`.
- Content: root workspace commands, checkpoint flags, opt-in history, verified sync semantics and shared team-token access were checked against the current README. No key issuance, Cloud, search or MCP features are implied.

## Findings and fixes

1. Narrow Quickstart initially moved ahead of the cookbook section only through CSS. Removed the visual reorder so keyboard and visual order agree. The hero's Start building anchor provides direct setup access.
2. History cookbook initially lacked an edit after enabling version capture. Added a post-enable write and sync before listing/exporting versions; verified the revised drawer in the browser.
3. Direct navigation to raw Markdown was handled as a download by the in-app browser. View now fetches and displays the actual bundled skill in the existing drawer; the separate SKILL.md download remains available. Verified the skill YAML/body in the rendered viewer.
4. The original banner and prompt were unnecessarily tall. Reduced their vertical footprint, then recaptured the complete composition. This remains an AFS adaptation with more cookbook detail than the reference.

No remaining actionable P0/P1/P2 design findings. Minor optional polish: a bespoke Markdown reader could present the skill more comfortably than the existing command drawer.

## Interaction verification

- Agent prompt and CLI switching; clipboard content exactly matches rendered text in both modes after success feedback settles.
- Checkpoint/history cookbook drawers, close actions and corrected instructions.
- API-access drawer explains the operator-provided shared token and never reveals or copies a credential.
- Skill viewer loads actual static content; downloadable asset remains linked as SKILL.md. In-app browser direct Markdown navigation is download-restricted, so download completion in an external browser was not exercised.
- Documentation, Monitor, and the narrow Start building anchor navigate correctly.
- Light/dark and 390px narrow layout inspected; explicit focus-visible styling provided for Home controls.
- Browser console contains no errors or warnings.

## Validation

`make control-plane`, UI lint, all 41 UI tests, Go build/vet, unit/race (1,027 passing cases each, four optional Array skips) and 208 isolated Redis integration cases passed. Logs: `/private/tmp/afs-home-*.log`. No existing user Redis or original installation was used.

## User correction and live verification — 2026-09-21

The user identified a P2 shell-height defect missed in the initial review: the
sidebar ended at its content height. Definite html/body/root heights and a
100dvh root fix the percentage-height chain. The active d39a checkout already
contained this correction; it is now also present in the preview source. Home
was merged into d39a, preserving all database-management work, then built and
restarted at http://127.0.0.1:8091/. Live DOM measurements: root/sidebar/viewport
all 820px; main client/scroll heights 768/1219px; scrolling moved only main
(348.5px), with body scroll and header top both zero. At 500px viewport height,
sidebar bottom remained 500px and the profile button occupied y395–441. Browser
console clean. Final result remains passed after this correction.

## Card consistency correction — 2026-09-21

Home now reuses SurfaceCard for its primary panels and inset cards, with solid
Classic panel backgrounds. User requested the same opaque surfaces as other
pages. Live browser computed fills are rgb(255,255,255) in light mode and
rgb(14,35,48) in dark mode; shared border radius is 4px in Situation Room.
Grid no longer shows through the cookbook and Quickstart panels. Existing
hero art and page content retained. Build/lint/75 UI tests pass.

## Hero card follow-up

The Learn Agent Filesystem hero now uses HomeCard/SurfaceCard. Verified on the rebuilt live instance: hero, AFS in action and Quickstart have identical computed opaque backgrounds, 1px borders, 4px radii and shadows in both light and dark themes. Text uses theme tokens and the illustration no longer uses multiply blending. Build, lint and 75 UI tests pass.

## Typography follow-up

All Home text increased: body 16–17px, recipe titles 20px, section headings 32px, labels at least 12px and prompt 13–14px. Larger responsive hero title, wider Quickstart and wrapping preserve legibility. Verified live at 1230px and 390px with no horizontal overflow. Build, lint and 75 UI tests pass.
