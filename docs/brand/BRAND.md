# Golden Gate — Design Philosophy

> A bridge you can see across, with a gate you control.

Golden Gate moves private work between two places. The design has one job: make
the user feel **in control of what crosses**. Everything below follows from that.

## 1. Principles

1. **Consent is visible.** State (shared, denied, pending) is always shown, never
   implied. The gate icon is open or closed; it is never ambiguous.
2. **Show the payload.** Prefer the real thing over a description of it. Page
   mockups use real previews with real redaction marks, not stock screenshots.
3. **Quiet by default.** Low saturation, generous space, one accent. Color
   carries meaning, so we spend it rarely.
4. **Nothing relies on color alone.** Every status pairs color with a glyph or
   label (matches the TUI's `NO_COLOR` promise). Contrast is AA at minimum.
5. **Terminal-native, not terminal-cosplay.** Monospace for commands, keys and
   data; a humanist sans for prose. No fake window chrome, no neon, no matrix rain.
6. **Honest and plain.** Say what leaves the machine. No "seamless", no
   "supercharge". Security copy is as prominent as feature copy.

## 2. Color

Fog, ink and one orange. The TUI theme (`internal/goldengate/tui/theme.go`) will
be updated to follow these tokens.

| Token | Light | Dark | Use |
|---|---|---|---|
| `--gg-bg` | `#F3F4F1` Fog | `#0C1116` Night | page background |
| `--gg-surface` | `#FFFFFF` | `#141B22` | cards, code blocks |
| `--gg-line` | `#D6DAD3` | `#26303A` | borders, dividers |
| `--gg-ink` | `#10161B` | `#E8ECEF` | text, marks, redaction bars |
| `--gg-dim` | `#58625E` | `#8F9A9F` | secondary text |
| `--gg-gate` | `#C8371A` | `#FF6A3D` | logo dot, primary button, links. Identity only |
| `--gg-on-gate` | `#FFFFFF` | `#1A0A04` | text on gate orange |
| `--gg-bay` | `#1D5C85` | `#78B6E3` | focus ring, keys, selection |
| `--gg-shared` | `#0F766E` | `#3CC9B5` | shared, healthy, added |
| `--gg-pending` | `#9A5B00` | `#F2B84B` | partial, changed |
| `--gg-error` | `#B3173B` | `#FF7A93` | failures only |

Rules
- Gate orange appears once per screen as a call to action, plus the logo dot.
  It never means warning or error.
- **Redaction is a black bar** (ink on ink), not red text. Red is for errors.
- **Denied is not an error.** It is struck through in ink, not flagged red.
- Light and dark are both designed. Flat backgrounds: no gradients, no glow.

## 3. Typography

- Prose and headings: **Schibsted Grotesk** (fallback `system-ui`): 800 display
  with tight tracking, 700 headings, 400 body.
- Commands, keys, paths, payloads: **IBM Plex Mono** (fallback `ui-monospace`).
- Scale (rem): 0.875 / 1 / 1.25 / 1.75 / 2.5 / 3.5. Line height 1.65 prose, 1.4 code. Measure 65ch.
- Keys render as `<kbd>`: mono, 1px `--gg-line` border with a 2px bottom edge, `--gg-bay` text.

## 4. Logo

The mark is a suspension bridge reduced to two towers, one cable and a deck.
The deck has a **gap in the middle with a single orange dot**: the gate, and the
one payload that crosses it. Files: `logo-mark.svg` (uses `currentColor` plus the
orange dot). Wordmark: "Golden Gate" in Schibsted Grotesk 800, mark to the left at cap-height x 1.6.

- Clear space: one tower-width on all sides. Minimum size 16px.
- Mono version: dot takes `currentColor`. Never recolor the towers orange.
- Don't: add gold gradients, rotate, outline, or put the mark in a circle.

## 5. Iconography

Set in `icons.svg` (sprite; use `<svg><use href="icons.svg#gg-shared"/></svg>`).

- 24px grid, 1.75px stroke, round caps and joins, `currentColor`, no fills.
- Metaphor: icons are built from the same bridge language where it fits.

| Icon | Meaning |
|---|---|
| `gg-shared` | project is shared (deck continuous, gate closed over it) |
| `gg-denied` | project denied (deck broken, gate open) |
| `gg-redact` | a redaction / what was removed |
| `gg-preview` | exact outgoing payload |
| `gg-sync` | push to server |
| `gg-project`, `gg-source`, `gg-server`, `gg-token`, `gg-assistant` | objects |

Pair state icons with text: "Shared", "Denied". Icon-only is allowed only with an
`aria-label` and tooltip.

### Terminal glyphs (page + TUI)

| State | Glyph | ASCII fallback |
|---|---|---|
| shared | `●` | `[x]` |
| not shared | `○` | `[ ]` |
| partial | `◐` | `[-]` |
| denied | `✕` | `[!]` |
| redacted | ink bar `█` | `###` |

## 6. Layout and motion

- Single column up to 72ch for prose; 1120px max for hero and feature grids.
- 8px spacing scale. Radius 6px (cards), 4px (kbd, chips). 1px borders, no shadows.
- Motion is functional only: the hero shows one dot crossing the gap, once, 1.4s,
  ease-out. Respect `prefers-reduced-motion` (static dot in the gap).

## 7. Project page outline

1. **Hero:** headline "Choose what your assistant knows." Sub: one sentence on
   what it does. Primary button (gate orange) "Quickstart", secondary "Read SECURITY.md".
2. **How it works:** laptop -> redact -> your server -> assistant, drawn with the
   bridge motif. Emphasize "your server".
3. **See exactly what is sent:** real preview payload with redaction marks.
4. **TUI tour:** real screenshots at 80x24, dark and light.
5. **Security and privacy:** what never leaves, how to revoke. Not hidden in a footer.
6. **Install / sources / roadmap.**

## 8. Voice

Short, concrete, second person. "Nothing is shared until you pick it." not
"Empower your workflow with seamless AI context."
