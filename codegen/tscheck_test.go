package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// frontEnd — зэргэлдээх front_end (zod, tsc) байвал замыг буцаана.
func frontEnd(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "front_end"))
	if err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "zod", "package.json")); err != nil {
		t.Skip("front_end/node_modules/zod алга — TS тест алгасав")
	}
	return dir
}

const stubKvTypes = `export interface KvFieldDef {
  name: string
  label?: string
  type?: string
  required?: boolean
  rules?: unknown
  options?: Array<{ label: string; value: unknown }>
  load?: (query: string) => Promise<any>
}
`

const stubAntRules = `export interface AntRule {
  required?: boolean
  message?: string
  pattern?: RegExp | string
  min?: number
  max?: number
  type?: string
  validator?: (rule: AntRule, value: any, callback?: (error?: unknown) => void) => unknown
}
`

const stubAxios = `const calls: Array<{ url: string; params: unknown }> = []
export const __calls = calls
export default {
  async get(url: string, cfg?: { params?: Record<string, unknown> }) {
    calls.push({ url, params: cfg?.params })
    return { data: { data: [{ value: 'a', label: 'A', parent_value: null }] } }
  },
}
`

const checkTS = `import { z } from 'zod'
import * as g from './gen'

// Payload interface ба zod schema харилцан таарна.
export const p1: g.ConferenceRoomPayload = {} as z.infer<typeof g.conferenceRoomSchema>
export const p2: z.input<typeof g.conferenceRoomSchema> = {} as g.ConferenceRoomPayload
export const p3: g.ConferenceRoomUpdatePayload = {} as z.infer<typeof g.conferenceRoomUpdateSchema>
export const p4: z.input<typeof g.conferenceRoomUpdateSchema> = {} as g.ConferenceRoomUpdatePayload
export const t1: g.TagPayload = {} as z.infer<typeof g.tagSchema>
export const t2: z.input<typeof g.tagSchema> = {} as g.TagPayload
export const t3: g.TagUpdatePayload = {} as z.infer<typeof g.tagUpdateSchema>
export const fields: import('./kvTypes').KvFieldDef[] = [...g.conferenceRoomFields, ...g.tagFields, ...g.conferenceRoomEquipmentFields]
export const row: g.ConferenceRoomRow['price'] = 12.5 // хариу: JSON тоо
export const pay: g.ConferenceRoomPayload['price'] = '12.50' // хүсэлт: тоо | мөр
`

const runTS = `import * as g from './gen'
import axios from './axios'

const ok = {
  room_name: 'Зал А', area: '12.5', meta: { floor: 2 }, secret: 'password1', starts_at: '2026-03-01T10:00:00+08:00',
  price: 1250.5, capacity: 30, status: 'open', code: 'a-1', opens: '2026-03-01',
  equipment: [{ item_id: '11111111-2222-3333-4444-555555555555', qty: 2, unit_price: '9.99' }],
}
function expectOk(v: unknown, label: string) {
  const r = g.conferenceRoomSchema.safeParse(v)
  if (!r.success) throw new Error(label + ' амжилтгүй: ' + JSON.stringify(r.error.issues))
}
function expectFail(v: unknown, label: string, path?: string) {
  const r = g.conferenceRoomSchema.safeParse(v)
  if (r.success) throw new Error(label + ' амжилттай болох ёсгүй')
  if (path && !r.error.issues.some((i) => i.path.join('.') === path)) throw new Error(label + ': path ' + path + ' олдсонгүй ' + JSON.stringify(r.error.issues))
}
expectOk(ok, 'ok')
expectOk({ ...ok, price: null, code: null }, 'nullable')
expectOk({ ...ok, price: '1.5' }, 'price мөр')
expectFail({ ...ok, price: 1.234 }, 'scale>2', 'price')
expectFail({ ...ok, price: '1.234' }, 'scale>2 мөр', 'price')
expectFail({ ...ok, price: -1 }, 'min', 'price')
expectFail({ ...ok, price: 'abc' }, 'тоо биш', 'price')
expectFail({ ...ok, area: '0.1' }, 'area min', 'area')
expectFail({ ...ok, area: '1.2345' }, 'area scale 3', 'area')
expectFail({ ...ok, room_name: '' }, 'required', 'room_name')
expectFail({ ...ok, room_name: 'x' }, 'minlen', 'room_name')
expectFail({ ...ok, code: '!!' }, 'pattern', 'code')
expectOk({ ...ok, code: 'ABC' }, 'pattern i flag')
expectFail({ ...ok, status: 'x' }, 'enum', 'status')
expectFail({ ...ok, opens: '2026/03/01' }, 'date', 'opens')
expectFail({ ...ok, starts_at: 'yesterday' }, 'datetime', 'starts_at')
expectFail({ ...ok, capacity: 1.5 }, 'int', 'capacity')
expectFail({ ...ok, bogus: 1 }, 'strict unknown')
expectFail({ ...ok, equipment: [{ item_id: 'zzz', qty: 1, unit_price: 1 }] }, 'child uuid', 'equipment.0.item_id')
expectFail({ ...ok, equipment: [{ item_id: '11111111-2222-3333-4444-555555555555', qty: 0, unit_price: 1 }] }, 'child min', 'equipment.0.qty')
const { meta: _m, ...noMeta } = ok
expectFail(noMeta, 'required json', 'meta')
const { secret: _s, ...noSecret } = ok
expectFail(noSecret, 'required secret', 'secret')

const u = g.conferenceRoomUpdateSchema
if (!u.safeParse({ version: 1, room_name: 'Зал Б' }).success) throw new Error('update ok')
if (u.safeParse({ room_name: 'Зал Б' }).success) throw new Error('update: version заавал')
if (u.safeParse({ version: 1, secret: 'password1' }).success) throw new Error('update: createOnly талбар strict-д татгалзагдах ёстой')

// Rules + load
const owner = g.conferenceRoomFields.find((f) => f.name === 'owner_id')!
const opts = await owner.load!('хайлт')
if (opts[0].value !== 'a') throw new Error('load')
const calls = (await import('./axios')).__calls
if (calls[0].url !== '/api/organization/conference/rooms/options/owner_id' || (calls[0].params as any).search !== 'хайлт') throw new Error('load url ' + JSON.stringify(calls))
if (g.conferenceRoomFields.find((f) => f.name === 'manager_id')!.load) throw new Error('employee override-д load байх ёсгүй')
const price = g.conferenceRoomFields.find((f) => f.name === 'price')!
if (price.type !== 'amount') throw new Error('price type')
const rule: any = (price.rules as any[])[0]
await rule.validator(rule, '1.50')
let failed = false
try { await rule.validator(rule, '1.555') } catch { failed = true }
if (!failed) throw new Error('rule scale')
if (g.conferenceRoomDefaults.status !== 'draft' || g.conferenceRoomDefaults.active !== true) throw new Error('defaults')
void axios
console.log('OK')
`

// TestTSTypeCheck — үүссэн TS-ийг жинхэнэ zod v4-тэй tsc-ээр шалгаж (strict, noUnusedLocals),
// bun байвал schema-г runtime-д ажиллуулна. front_end/node_modules эсвэл tsc алга бол skip.
func TestTSTypeCheck(t *testing.T) {
	fe := frontEnd(t)
	tsc := filepath.Join(fe, "node_modules", ".bin", "tsc")
	if p, err := exec.LookPath("tsc"); err == nil {
		tsc = p
	} else if _, err := os.Stat(tsc); err != nil {
		t.Skip("tsc алга")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(fe, "node_modules"), filepath.Join(dir, "node_modules")); err != nil {
		t.Skip(err)
	}
	out, err := TS(sampleMetas(), TSOptions{AxiosImport: "./axios", KvFieldImport: "./kvTypes", AntRuleImport: "./antRules"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"gen.ts": string(out), "kvTypes.ts": stubKvTypes, "antRules.ts": stubAntRules, "axios.ts": stubAxios,
		"check.ts": checkTS, "run.ts": runTS,
		"tsconfig.json": `{"compilerOptions":{"strict":true,"noUnusedLocals":true,"noUnusedParameters":true,"noEmit":true,"target":"ES2022","module":"ESNext","moduleResolution":"bundler","skipLibCheck":true,"types":[]},"include":["*.ts"]}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(tsc, "-p", dir)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s", err, b)
	}

	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Log("bun алга — runtime шалгалтыг алгасав")
		return
	}
	run := exec.Command(bun, "run.ts")
	run.Dir = dir
	if b, err := run.CombinedOutput(); err != nil {
		t.Fatalf("bun run: %v\n%s", err, b)
	}
}
