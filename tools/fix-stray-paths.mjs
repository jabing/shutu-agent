// One-shot fix for stray replacements left by rename-module.mjs:
//  - D:\dev-projects\Agent\shutu-agent (directory paths in
//    docs/tools)  ->  D:\dev-projects\Agent\shutu-agent
//  - "shutu-agent/0.1 (M7)" (User-Agent strings, not imports)
//    -> "shutu-agent/0.1 (M7)"
//  - dev@shutu-agent.local  ->  dev@shutu-agent.local
import fs from 'node:fs'
import path from 'node:path'

const root = process.argv[2] ?? '.'
const TEXT = new Set(['.go', '.md', '.html', '.js', '.mjs', '.css', '.yml', '.yaml', '.json', '.toml', '.mod'])
const skipDir = (p) =>
  p.includes(`${path.sep}.gomodcache`) || p.includes(`${path.sep}.gocache`) ||
  p.includes(`${path.sep}.git`) || p.includes(`${path.sep}node_modules`)

function fix(s) {
  return s
    .replaceAll('D:\\dev-projects\\Agent\\github.com\\jabing\\shutu-agent', 'D:\\dev-projects\\Agent\\shutu-agent')
    .replaceAll('D:/dev-projects/Agent/shutu-agent', 'D:/dev-projects/Agent/shutu-agent')
    .replaceAll('shutu-agent/0.1 (M7)', 'shutu-agent/0.1 (M7)')
    .replaceAll('dev@shutu-agent.local', 'dev@shutu-agent.local')
}

let fixed = 0
function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name)
    if (skipDir(p)) continue
    if (e.isDirectory()) walk(p)
    else if (TEXT.has(path.extname(e.name))) {
      let c
      try { c = fs.readFileSync(p, 'utf8') } catch { continue }
      const n = fix(c)
      if (n !== c) { fs.writeFileSync(p, n, 'utf8'); fixed++; console.log('fixed', p) }
    }
  }
}
walk(root)
console.log('files fixed:', fixed)
