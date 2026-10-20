# Golden Gate — Design Philosophy

> A bridge you can see across.

Golden Gate moves private work between two places. The design has one job: make
the user feel **in control of what crosses**. Everything below follows from that.

## 1. Principles

1. **Consent is visible.** State (shared, denied, pending) is always shown, never
   implied. The shared icon is the full cable, denied is the cable cut; never ambiguous.
2. **Show the payload.** Prefer the real thing over a description of it. Page
   mockups use real previews with real redaction marks, not stock screenshots.
3. **Quiet by default.** Low saturation, generous space, one accent. Color
   carries meaning, so we spend it rarely.
4. **Nothing relies on color alone.** Every status pairs color with a glyph or
   label (matches the TUI's `NO_COLOR` promise). Contrast is AA at minimum.
5. **Terminal-native, not terminal-cosplay.** Monospace for commands, keys and
   data; Art Deco faces for words (Federo, Josefin Sans). No fake window chrome, no neon, no matrix rain.
6. **Honest and plain.** Say what leaves the machine. No "seamless", no
   "supercharge". Security copy is as prominent as feature copy.

## 2. Color

Painted from the bridge. The TUI theme (`internal/goldengate/tui/theme.go`) will follow these tokens.

| Token | Name | Light | Dark | Use |
|---|---|---|---|---|
| `--gg-orange` | International Orange | `#D03A2B` | `#F0563F` | the mark, primary button, links. Identity only |
| `--gg-on-orange` | | `#FFFFFF` | `#1B0703` | text on orange |
| `--gg-bg` | Fog / Night bay | `#EEF0EE` | `#0D1821` | page ground |
| `--gg-surface` | Surface | `#FFFFFF` | `#132330` | cards, code |
| `--gg-sunk` | | `#DCE8F0` | `#0A131A` | selected tabs, insets |
| `--gg-line` | Cable line | `#93A3AB` | `#233646` | borders |
| `--gg-ink` | Ink | `#13202A` | `#E5EBEE` | text, marks, redaction bars |
| `--gg-dim` | Marine layer | `#4A5960` | `#8D9DA7` | secondary text |
| `--gg-pacific` | Pacific | `#0B5F94` | `#7FBADB` | focus ring, keys |
| `--gg-cypress` | Cypress | `#137A44` | `#72C59A` | shared, healthy, added |
| `--gg-headland` | Headland | `#B05A00` | `#E8B652` | partial, pending, changed |
| `--gg-alarm` | Alarm | `#B8165F` | `#F27EB2` | failures; denied glyph only |
| `--gg-twilight` | Twilight | `#6A35C2` | `#B9A0E6` | chat-source tags (a kind, never a state) |
| `--gg-term-bg` | | `#F8F9F8` | `#09131B` | terminal background |
| `--gg-term-cur` | | `#C6DDEE` | `#1B3446` | TUI cursor row |

Rules
- International Orange is the bridge's own paint. It never means warning or error.
- Redaction is an ink bar (ink on ink), not red text.
- Denied is a decision: Alarm on the glyph only, the row stays in ink.

## 3. Typography

Art Deco, from the era the bridge opened in (1937).

| Role | Face | Weights | Fallback |
|---|---|---|---|
| Display: wordmark, h1–h3 | **Federo** | 400 (its only weight; never fake bold) | `"Josefin Sans", system-ui, sans-serif` |
| Text: body, UI labels, buttons | **Josefin Sans** | 400 body, 600 emphasis and buttons | `system-ui, sans-serif` |
| Mono: commands, keys, paths, payloads | **IBM Plex Mono** | 400, 500 | `ui-monospace, Menlo, monospace` |

```html
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Federo&family=Josefin+Sans:wght@400;600&family=IBM+Plex+Mono:wght@400;500&display=swap">
```

Rules
- Federo is for display only, at 1.1rem and up, letter-spacing `+0.02em`. Never set
  body text, long paragraphs or anything under 16px in it.
- Josefin Sans has a small x-height: set body at 17px minimum, line height 1.65,
  measure 65ch. Use 600 for emphasis instead of bold 700.
- Uppercase labels and eyebrows use Plex Mono 500, 0.72rem, tracking `0.12em`.
- Scale (rem): 0.875 / 1 / 1.1 / 1.8 / 2.6 / 4. Code line height 1.4.
- Keys render as `<kbd>`: mono, 1px `--gg-line` border with a 2px bottom edge, `--gg-pacific` text.
- The TUI uses the terminal's own font; these faces apply to the web, docs and graphics.

## 4. Logo

Two towers and the suspension cable, curved in all three spans (side spans sag
to anchors, main span dips between the towers). No deck, no dot. File:
`logo-mark.svg` (64x48 box, towers at x=20 and x=44).
Wordmark: "Golden Gate" in Federo, mark to the left at 1.6x the cap height.

- Primary in International Orange; mono in the current text color; reversed on orange.
- One stroke weight for towers and cable, round caps: 3.2 at 48px, 2 at 16px.
- Clear space: one tower-width. Minimum 16px.
- Don't add a deck, a gold gradient, a circle, an outline, or two colors.

## 5. Iconography

Set in `icons.svg` (sprite; use `<svg><use href="icons.svg#gg-shared"/></svg>`).

- 24px grid, 1.75px stroke, round caps and joins, `currentColor`, no fills.
- Metaphor: icons are built from the same bridge language where it fits.

| Icon | Meaning |
|---|---|
| `gg-shared` | project is shared (the full cable) |
| `gg-denied` | project denied (the cable cut in the middle) |
| `gg-redact` | a redaction / what was removed |
| `gg-preview` | exact outgoing payload |
| `gg-sync` | push to server |
| `gg-project`, `gg-source`, `gg-server`, `gg-token`, `gg-assistant` | objects |

Pair state icons with text: "Shared", "Denied". Icon-only is allowed only with an
`aria-label` and tooltip.

### State glyphs

The TUI uses brackets, the web uses round glyphs. Both always carry the word.

| State | TUI | Web | Color |
|---|---|---|---|
| shared | `[x]` | `●` | Cypress |
| not shared | `[ ]` | `○` | Ink |
| partial | `[~]` | `◐` | Headland |
| denied | `[-]` | `✕` | Alarm, glyph only |
| pending | `▲ 3` | `▲ 3` | Headland |
| redacted | ink bar | ink bar | Ink on ink |

### Source tags

Every session and project carries the tag of the tool it came from. The tag is
text, so it reads without color; the color says the kind.

| Tag | Source | Kind | Color |
|---|---|---|---|
| `CC` | Claude Code | code | Pacific |
| `CX` | Codex | code | Pacific |
| `OC` | OpenCode | code | Pacific |
| `AI` | claude.ai export | chat | Twilight |
| `GPT` | ChatGPT export | chat | Twilight |

## 6. Layout and motion

- Single column up to 72ch for prose; 1120px max for hero and feature grids.
- 8px spacing scale. Radius 8px (cards), 4px (kbd, chips). 1px borders, no shadows.
- Motion: on the project page the cable draws itself once (under 2s) and fog drifts
  slowly through the towers. Nothing else moves. Respect `prefers-reduced-motion`.

## 7. Project page outline

1. **Hero:** headline "Choose what your assistant knows." Sub: one sentence on
   what it does. Primary button (International Orange) "Quickstart", secondary "Read SECURITY.md".
2. **How it works:** laptop -> redact -> your server -> assistant, drawn with the
   bridge motif. Emphasize "your server".
3. **See exactly what is sent:** real preview payload with redaction marks.
4. **TUI tour:** real screenshots at 80x24, dark and light.
5. **Security and privacy:** what never leaves, how to revoke. Not hidden in a footer.
6. **Install / sources / roadmap.**

## 8. Voice

Short, concrete, second person. "Nothing is shared until you pick it." not
"Empower your workflow with seamless AI context."
