// One-shot extractor: pulls the exact dsh web icon SVGs (ui-primitives
// icons/index.tsx + FishLogo.tsx) into internal/webserver/static/icons.js as a
// plain ICONS map of SVG strings. User asked to reuse the dsh web icons as-is.
// JSX attribute names are converted to HTML; sizing is left to CSS via
// viewBox (width/height omitted).
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'

const root = 'D:/dev-projects/Agent/deepseek-harness/packages/client/ui-primitives/src'
const out = resolve('D:/dev-projects/Agent/personal-agent/internal/webserver/static/icons.js')

// Icons used by the sidebar / workspace grouping (from ui-sidebar + ui-workspace
// imports) — plus the FishLogo brand mark and the settings gear for the foot.
const WANT = new Set([
  'FishLogo',
  'IconPanelLeftOutline16',
  'IconNewChatOutline16',
  'IconSearchOutline16',
  'IconCloseFill14',
  'IconPersonalizationOutline16',
  'IconProjectAddOutline16',
  'IconFolderClose16',
  'IconFolderOpen16',
  'IconTriangleRightFill14',
  'IconEllipsisOutline16',
  'IconPlusOutline16',
  'IconEditOutline16',
  'IconTrashOutline16',
  'IconBranchOutline16',
  'IconArchiveOutline20',
  'IconSettingsOutline16',
])

const indexSrc = readFileSync(resolve(root, 'icons/index.tsx'), 'utf8')
const fishSrc = readFileSync(resolve(root, 'FishLogo.tsx'), 'utf8')

function jsxAttrsToHtml(svg) {
  return svg
    .replace(/\s(width|height|className)=\{[^}]*\}/g, '')
    .replace(/strokeWidth/g, 'stroke-width')
    .replace(/strokeLinecap/g, 'stroke-linecap')
    .replace(/strokeLinejoin/g, 'stroke-linejoin')
    .replace(/fillRule/g, 'fill-rule')
    .replace(/clipRule/g, 'clip-rule')
    .replace(/\s*\/>\s*$/m, ' />')
}

function extractSvg(src, name) {
  // Matches `export const Name = ...` or `export function Name(...)` then the
  // first `</svg>`.
  const head = src.indexOf(`export ${name === 'FishLogo' ? 'function' : 'const'} ${name}`)
  if (head === -1) throw new Error(`icon not found: ${name}`)
  const open = src.indexOf('<svg', head)
  const close = src.indexOf('</svg>', open) + '</svg>'.length
  return src.slice(open, close)
}

const icons = {}
for (const name of WANT) {
  let svg
  if (name === 'FishLogo') {
    svg = extractSvg(fishSrc, name)
  } else {
    svg = extractSvg(indexSrc, name)
  }
  const cleaned = jsxAttrsToHtml(svg)
    .replace(/\s+/g, ' ')
    .trim()
  const key = name.replace(/^Icon/, '').replace(/Outline\d+$|Fill\d+$/i, '').toLowerCase() || 'brand'
  icons[key] = cleaned
}

const body = `/* Extracted verbatim from the dsh web icon set (client/ui-primitives
   icons/index.tsx + FishLogo.tsx) — user requested reuse of the same icons.
   Each entry is a complete <svg>; sizing rides CSS (viewBox-based). */
const PA_ICONS = ${JSON.stringify(icons, null, 2).replace(/"((?:\\"|[^"])*)"/g, "'$1'")};
`
writeFileSync(out, body)
console.log('wrote', out, Object.keys(icons).length, 'icons:', Object.keys(icons).join(', '))
