package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// OwnerPolicy — Grants.Owner-ийг хэрхэн тайлах.
type OwnerPolicy int

const (
	// OwnerAll (анхдагч) — owner нь бүх модулийн БҮХ capability-тай; deny үүнийг хасахгүй
	// (эзнийг санамсаргүй түгжихгүй). Manifest-д байхгүй capability-д owner ч татгалзагдана.
	OwnerAll OwnerPolicy = iota
	// OwnerDeniable — owner бүх capability-тай, ГЭХДЭЭ тодорхой deny хэвээр хасагдана.
	OwnerDeniable
	// OwnerNone — owner тэмдэглэгээг үл тооно; бусад хэрэглэгчтэй адил role/allow/deny-ээр шийдэнэ.
	OwnerNone
)

// Transition — "модуль байгууллагад тохируулагдаагүй" үеийн шилжилтийн горим.
// Анхдагч нь [TransitionOff] (strict). Энэ нь main-ийн RequireCapabilityOrUnconfigured-ийн
// тод (explicit) opt-in хувилбар: хуучин "нэвтэрсэн бол болно" route-уудыг эрх тохируулах
// хүртэл нээлттэй үлдээж, тохируулмагц автоматаар хаадаг.
type Transition int

const (
	// TransitionOff — шилжилтийн горим унтраалттай (strict).
	TransitionOff Transition = iota
	// TransitionUser — хэрэглэгчид тухайн модулийн блок (role/allow/deny) огт байхгүй бол нээлттэй.
	TransitionUser
	// TransitionOrg — байгууллагын НЭГ Ч хэрэглэгчид тухайн модулийн эрх олгоогүй бол нээлттэй.
	// Source нь [OrgConfigurer] байх ёстой ([New] шалгана).
	TransitionOrg
)

// Reason — шийдвэрийн шалтгаан (лог, тест, debug).
type Reason string

// Шийдвэрийн шалтгаанууд.
const (
	ReasonOwner      Reason = "owner"
	ReasonGranted    Reason = "granted"
	ReasonTransition Reason = "transition"
	ReasonDenied     Reason = "denied"      // явного deny
	ReasonNoGrant    Reason = "no_grant"    // эрх олгоогүй
	ReasonUnknown    Reason = "unknown"     // модуль/capability manifest-д алга
	ReasonNoIdentity Reason = "no_identity" // org/user тодорхойгүй
	ReasonError      Reason = "error"       // Source алдаа (fail-closed)
)

// Decision — нэг шалгалтын үр дүн.
type Decision struct {
	Allowed bool
	Reason  Reason
	// Capability — зөвшөөрсөн (эсвэл татгалзсан) capability (any-of дундаас).
	Capability string
}

// Policy — compile хийсэн, concurrent-safe эрхийн шийдвэр гаргагч. [New]-ээр үүснэ.
type Policy struct {
	reg         *Registry
	comp        *compiled
	src         Source
	log         *slog.Logger
	owner       OwnerPolicy
	trans       map[string]Transition
	users       *loader[*resolved]
	orgs        *loader[map[string]bool]
	gens        Generations
	prefix      string
	denyHook    DenyHook
	loadTimeout time.Duration
}

type options struct {
	now         func() time.Time
	ttl         time.Duration
	errTTL      time.Duration
	orgTTL      time.Duration
	maxEntries  int
	loadTimeout time.Duration
	gens        Generations
	genPrefix   string
	genInterval time.Duration
	owner       OwnerPolicy
	trans       map[string]Transition
	log         *slog.Logger
	denyHook    DenyHook
	errs        []error
}

// Option — [New]-ийн тохиргоо.
type Option func(*options)

// Анхдагч утгууд.
const (
	DefaultTTL         = 5 * time.Second
	DefaultErrorTTL    = time.Second
	DefaultOrgTTL      = 30 * time.Second
	DefaultMaxEntries  = 50_000
	DefaultLoadTimeout = 5 * time.Second
	DefaultGenInterval = time.Second
	DefaultGenPrefix   = "khan:access"
)

// WithTTL — хэрэглэгчийн эрхийн cache-ийн наслалт (анхдагч 5с). 0 → cache хийхгүй.
func WithTTL(d time.Duration) Option {
	return func(o *options) {
		if d < 0 {
			o.errs = append(o.errs, errors.New("access: TTL сөрөг"))
		}
		o.ttl = d
	}
}

// WithOrgTTL — org-ийн "тохируулсан модулиуд"-ын cache (анхдагч 30с). Зөвхөн TransitionOrg-д.
func WithOrgTTL(d time.Duration) Option {
	return func(o *options) {
		if d < 0 {
			o.errs = append(o.errs, errors.New("access: org TTL сөрөг"))
		}
		o.orgTTL = d
	}
}

// WithErrorTTL — Source алдааг хэр хугацаагаар cache-лэх (анхдагч 1с). Source унасан үед
// түүнийг хүсэлт бүрээр дэвслэхээс хамгаалдаг. 0 → алдаа cache хийхгүй.
func WithErrorTTL(d time.Duration) Option {
	return func(o *options) {
		if d < 0 {
			o.errs = append(o.errs, errors.New("access: error TTL сөрөг"))
		}
		o.errTTL = d
	}
}

// WithClock — цаг (тестэд fake clock).
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now == nil {
			o.errs = append(o.errs, errors.New("access: clock nil"))
			return
		}
		o.now = now
	}
}

// WithLogger — Source алдаа зэргийг бичих logger (анхдагч slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.log = l
		}
	}
}

// WithMaxEntries — cache-ийн дээд хэмжээ (анхдагч 50k хэрэглэгч). Хэтэрвэл хугацаа
// дууссан, дараа нь санамсаргүй 10% хасагдана.
func WithMaxEntries(n int) Option {
	return func(o *options) {
		if n <= 0 {
			o.errs = append(o.errs, errors.New("access: MaxEntries > 0 байх ёстой"))
		}
		o.maxEntries = n
	}
}

// WithLoadTimeout — Source.Load-ийн дээд хугацаа (анхдагч 5с).
func WithLoadTimeout(d time.Duration) Option {
	return func(o *options) {
		if d <= 0 {
			o.errs = append(o.errs, errors.New("access: LoadTimeout > 0 байх ёстой"))
		}
		o.loadTimeout = d
	}
}

// WithOwnerPolicy — owner-ийг хэрхэн тайлах (анхдагч [OwnerAll]).
func WithOwnerPolicy(p OwnerPolicy) Option {
	return func(o *options) {
		if p < OwnerAll || p > OwnerNone {
			o.errs = append(o.errs, fmt.Errorf("access: OwnerPolicy %d буруу", p))
		}
		o.owner = p
	}
}

// WithTransition — тухайн модулийн шилжилтийн горим (тод opt-in; анхдагч strict).
// Модуль Registry-д байх ёстой, TransitionOrg-д Source нь [OrgConfigurer] байх ёстой.
func WithTransition(module string, mode Transition) Option {
	return func(o *options) {
		if mode < TransitionOff || mode > TransitionOrg {
			o.errs = append(o.errs, fmt.Errorf("access: %s: Transition %d буруу", module, mode))
			return
		}
		o.trans[module] = mode
	}
}

// WithGenerations — олон instance-ийн invalidation: [Policy.Invalidate] нь хуваалцсан
// generation-ийг нэмэгдүүлж, бусад instance cache-тэй entry бүрийн generation-ийг
// `interval` (анхдагч 1с) тутамд шалгаж зөрвөл дахин ачаална. prefix = түлхүүрийн угтвар
// (хоосон → [DefaultGenPrefix]); interval ≤ 0 → [DefaultGenInterval]. Generation уншигдахгүй
// бол entry дахин ачаалагдана (хуучин эрх үлдэхгүй).
func WithGenerations(g Generations, prefix string, interval time.Duration) Option {
	return func(o *options) {
		if g == nil {
			o.errs = append(o.errs, errors.New("access: Generations nil"))
			return
		}
		o.gens = g
		if prefix != "" {
			o.genPrefix = prefix
		}
		if interval > 0 {
			o.genInterval = interval
		}
	}
}

// New — Registry-г хөлдөөж compile хийн Policy үүсгэнэ. Registry хоосон, Source nil,
// буруу тохиргоо бол алдаа.
func New(reg *Registry, src Source, opts ...Option) (*Policy, error) {
	if reg == nil || reg.Len() == 0 {
		return nil, errors.New("access: Registry хоосон")
	}
	if src == nil {
		return nil, errors.New("access: Source nil")
	}
	o := &options{
		now: time.Now, ttl: DefaultTTL, errTTL: DefaultErrorTTL, orgTTL: DefaultOrgTTL,
		maxEntries: DefaultMaxEntries, loadTimeout: DefaultLoadTimeout,
		genPrefix: DefaultGenPrefix, genInterval: DefaultGenInterval,
		trans: map[string]Transition{}, log: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	if err := errors.Join(o.errs...); err != nil {
		return nil, err
	}
	reg.Freeze()
	comp := compile(reg)
	for mod, mode := range o.trans {
		if _, ok := comp.modules[mod]; !ok {
			return nil, fmt.Errorf("access: transition: модуль %q manifest-д алга", mod)
		}
		if mode == TransitionOrg {
			if _, ok := src.(OrgConfigurer); !ok {
				return nil, fmt.Errorf("access: %s: TransitionOrg-д Source нь OrgConfigurer байх ёстой", mod)
			}
		}
	}
	orgOpts := *o
	orgOpts.ttl = o.orgTTL
	orgOpts.errTTL = o.errTTL
	return &Policy{
		reg: reg, comp: comp, src: src, log: o.log, owner: o.owner, trans: o.trans,
		users: newLoader[*resolved](o), orgs: newLoader[map[string]bool](&orgOpts),
		gens: o.gens, prefix: o.genPrefix, denyHook: o.denyHook, loadTimeout: o.loadTimeout,
	}, nil
}

// Registry — Policy-ийн (хөлдсөн) Registry.
func (p *Policy) Registry() *Registry { return p.reg }

// ─── compile ───

type cmodule struct {
	manifest Manifest
	roles    map[string]struct{}
	caps     map[string]struct{}
	capList  []string            // эрэмбэлсэн
	defaults map[string][]string // role → default caps
}

type compiled struct {
	modules map[string]*cmodule
	order   []string
}

func compile(reg *Registry) *compiled {
	c := &compiled{modules: map[string]*cmodule{}}
	for _, m := range reg.All() {
		cm := &cmodule{manifest: m, roles: map[string]struct{}{}, caps: map[string]struct{}{}, defaults: map[string][]string{}}
		for _, r := range m.Roles {
			cm.roles[r.Key] = struct{}{}
			cm.defaults[r.Key] = m.DefaultCapabilitiesFor(r.Key)
		}
		for _, cp := range m.Capabilities {
			cm.caps[cp.Key] = struct{}{}
			cm.capList = append(cm.capList, cp.Key)
		}
		sort.Strings(cm.capList)
		c.modules[m.Key] = cm
		c.order = append(c.order, m.Key)
	}
	return c
}

// resolved — нэг хэрэглэгчийн compile хийсэн (immutable) эрх.
type resolved struct {
	owner bool
	mods  map[string]*modAccess
}

type modAccess struct {
	roles      []string // manifest-д байгаа role-ууд
	caps       map[string]struct{}
	denied     map[string]struct{}
	configured bool // блок хоосон биш
}

// resolve — Grants-ийг manifest-тэй тулгаж effective capability болгоно:
// (role-уудын анхдагч ∪ allow) ∖ deny. Manifest-д байхгүй модуль/role/capability алгасагдана.
func (c *compiled) resolve(g Grants) *resolved {
	r := &resolved{owner: g.Owner, mods: make(map[string]*modAccess, len(g.Modules))}
	for key, mg := range g.Modules {
		cm, ok := c.modules[key]
		if !ok {
			continue
		}
		ma := &modAccess{caps: map[string]struct{}{}, denied: map[string]struct{}{}, configured: !mg.Empty()}
		for _, role := range mg.Roles {
			if _, ok := cm.roles[role]; !ok {
				continue
			}
			ma.roles = append(ma.roles, role)
			for _, cp := range cm.defaults[role] {
				ma.caps[cp] = struct{}{}
			}
		}
		for _, cp := range mg.Allow {
			if _, ok := cm.caps[cp]; ok {
				ma.caps[cp] = struct{}{}
			}
		}
		for _, cp := range mg.Deny {
			if _, ok := cm.caps[cp]; ok {
				ma.denied[cp] = struct{}{}
				delete(ma.caps, cp)
			}
		}
		sort.Strings(ma.roles)
		r.mods[key] = ma
	}
	return r
}

// ─── loading ───

func userKey(org, user string) string { return org + "\x00" + user }

func (p *Policy) genKeyOrg(org string) string { return p.prefix + ":org:" + org }
func (p *Policy) genKeyUser(org, user string) string {
	return p.prefix + ":user:" + org + ":" + user
}
func (p *Policy) genKeyCfg(org string) string { return p.prefix + ":cfg:" + org }

func (p *Policy) resolveUser(ctx context.Context, org, user string) (*resolved, error) {
	return p.users.get(ctx, org, userKey(org, user),
		[]string{p.genKeyOrg(org), p.genKeyUser(org, user)},
		func(ctx context.Context) (*resolved, error) {
			g, err := p.src.Load(ctx, org, user)
			if err != nil {
				return nil, err
			}
			return p.comp.resolve(g), nil
		})
}

func (p *Policy) configuredModules(ctx context.Context, org string) (map[string]bool, error) {
	oc, ok := p.src.(OrgConfigurer)
	if !ok {
		return nil, errors.New("access: Source OrgConfigurer биш")
	}
	return p.orgs.get(ctx, org, org,
		[]string{p.genKeyOrg(org), p.genKeyCfg(org)},
		func(ctx context.Context) (map[string]bool, error) { return oc.ConfiguredModules(ctx, org) })
}

// ─── decision ───

// Verify — module болон capability-ууд manifest-д байгааг шалгана (апп эхлэхэд, route-ын
// тодорхойлолтын үсгийн алдааг барих). Хоосон caps бол алдаа.
func (p *Policy) Verify(module string, caps ...string) error {
	cm, ok := p.comp.modules[module]
	if !ok {
		return fmt.Errorf("access: модуль %q manifest-д алга", module)
	}
	if len(caps) == 0 {
		return fmt.Errorf("access: %s: capability заагаагүй", module)
	}
	for _, c := range caps {
		if _, ok := cm.caps[c]; !ok {
			return fmt.Errorf("access: %s: capability %q manifest-д алга", module, c)
		}
	}
	return nil
}

// decide — цэвэр шийдвэр (res ачаалагдсан). any-of: caps-ийн аль нэг хангалттай.
// portal=true бол (сурагч/эцэг эхийн portal token) шилжилтийн горим үйлчлэхгүй.
func (p *Policy) decide(ctx context.Context, org string, res *resolved, portal bool, module string, caps []string) (Decision, error) {
	cm, ok := p.comp.modules[module]
	if !ok {
		return Decision{Reason: ReasonUnknown}, nil
	}
	known := make([]string, 0, len(caps))
	for _, c := range caps {
		if _, ok := cm.caps[c]; ok {
			known = append(known, c)
		}
	}
	if len(known) == 0 {
		return Decision{Reason: ReasonUnknown}, nil
	}
	ma := res.mods[module]
	if res.owner && p.owner != OwnerNone {
		if p.owner == OwnerAll {
			return Decision{Allowed: true, Reason: ReasonOwner, Capability: known[0]}, nil
		}
		for _, c := range known {
			if ma == nil || !has(ma.denied, c) {
				return Decision{Allowed: true, Reason: ReasonOwner, Capability: c}, nil
			}
		}
		return Decision{Reason: ReasonDenied, Capability: known[0]}, nil
	}
	if ma != nil {
		for _, c := range known {
			if has(ma.caps, c) {
				return Decision{Allowed: true, Reason: ReasonGranted, Capability: c}, nil
			}
		}
		for _, c := range known {
			if has(ma.denied, c) {
				return Decision{Reason: ReasonDenied, Capability: c}, nil
			}
		}
	}
	if !portal {
		switch p.trans[module] {
		case TransitionUser:
			if ma == nil || !ma.configured {
				return Decision{Allowed: true, Reason: ReasonTransition, Capability: known[0]}, nil
			}
		case TransitionOrg:
			mods, err := p.configuredModules(ctx, org)
			if err != nil {
				return Decision{Reason: ReasonError}, err
			}
			if !mods[module] {
				return Decision{Allowed: true, Reason: ReasonTransition, Capability: known[0]}, nil
			}
		}
	}
	return Decision{Reason: ReasonNoGrant, Capability: known[0]}, nil
}

func has(m map[string]struct{}, k string) bool { _, ok := m[k]; return ok }

// Check — (org, user) нь module-ийн caps-ийн АЛЬ НЭГИЙГ хийж чадах эсэх (HTTP-гүй: job, мэдэгдэл).
// Source алдаа бол Decision{Allowed:false, Reason:ReasonError} ба алдааг буцаана (лог бичигдэнэ).
func (p *Policy) Check(ctx context.Context, org, user, module string, caps ...string) (Decision, error) {
	if org == "" || user == "" {
		return Decision{Reason: ReasonNoIdentity}, nil
	}
	res, err := p.resolveUser(ctx, org, user)
	if err != nil {
		p.logErr(ctx, p.log, org, user, err)
		return Decision{Reason: ReasonError}, err
	}
	d, err := p.decide(ctx, org, res, false, module, caps)
	if err != nil {
		p.logErr(ctx, p.log, org, user, err)
	}
	return d, err
}

// Allowed — Check-ийн товч хэлбэр: алдаа гарвал false.
func (p *Policy) Allowed(ctx context.Context, org, user, module string, caps ...string) bool {
	d, err := p.Check(ctx, org, user, module, caps...)
	return err == nil && d.Allowed
}

func (p *Policy) logErr(ctx context.Context, l *slog.Logger, org, user string, err error) {
	l.ErrorContext(ctx, "access: эрх шалгаж чадсангүй — татгалзав (fail-closed)",
		"org_id", org, "user_id", user, "error", err)
}

// ─── snapshot ───

// ModuleSnapshot — нэг модулийн эрх (frontend-д).
type ModuleSnapshot struct {
	// Role — үндсэн role (Roles[0]); main/usePermission.ts-ийн `role`. Owner → "owner".
	Role string `json:"role"`
	// Roles — бүх role (шинэ; олон role-ийг дэмжинэ).
	Roles []string `json:"roles"`
	// Capabilities — effective capability-ууд, эрэмбэлсэн, хэзээ ч null биш.
	Capabilities []string `json:"capabilities"`
}

// Snapshot — frontend-ийн `/me` хариу: main-ийн `{is_owner, modules}` хэлбэртэй ижил
// (usePermission.ts: `modules[key].role`, `.capabilities`).
type Snapshot struct {
	Owner   bool                      `json:"is_owner"`
	Modules map[string]ModuleSnapshot `json:"modules"`
}

// Snapshot — (org, user)-ийн effective эрх. Шилжилтийн горим энд тусгагдахгүй (зөвхөн
// сервер талын gate) — main-ийн /me-тэй ижил.
func (p *Policy) Snapshot(ctx context.Context, org, user string) (Snapshot, error) {
	snap := Snapshot{Modules: map[string]ModuleSnapshot{}}
	if org == "" || user == "" {
		return snap, ErrNoOrg
	}
	res, err := p.resolveUser(ctx, org, user)
	if err != nil {
		return snap, err
	}
	return p.snapshotOf(res), nil
}

func (p *Policy) snapshotOf(res *resolved) Snapshot {
	snap := Snapshot{Modules: map[string]ModuleSnapshot{}}
	ownerAll := res.owner && p.owner != OwnerNone
	snap.Owner = ownerAll
	for _, key := range p.comp.order {
		cm := p.comp.modules[key]
		ma := res.mods[key]
		if ownerAll {
			caps := make([]string, 0, len(cm.capList))
			for _, c := range cm.capList {
				if p.owner == OwnerDeniable && ma != nil && has(ma.denied, c) {
					continue
				}
				caps = append(caps, c)
			}
			snap.Modules[key] = ModuleSnapshot{Role: "owner", Roles: []string{"owner"}, Capabilities: caps}
			continue
		}
		if ma == nil || (len(ma.roles) == 0 && len(ma.caps) == 0) {
			continue
		}
		caps := make([]string, 0, len(ma.caps))
		for c := range ma.caps {
			caps = append(caps, c)
		}
		sort.Strings(caps)
		roles := append([]string{}, ma.roles...)
		role := ""
		if len(roles) > 0 {
			role = roles[0]
		}
		snap.Modules[key] = ModuleSnapshot{Role: role, Roles: roles, Capabilities: caps}
	}
	return snap
}

// ─── invalidation ───

// Invalidate — (org, user)-ийн cache-ийг шууд хүчингүй болгоно (эрх өөрчлөгдсөний дараа дуудна).
// Generations тохируулсан бол бусад instance-д мэдэгдэнэ; тэр алдаатай бол буцаана
// (локал cache аль хэдийн цэвэр; бусад instance TTL-ээр хүрнэ).
func (p *Policy) Invalidate(ctx context.Context, org, user string) error {
	p.users.invalidate(userKey(org, user))
	p.orgs.invalidate(org)
	return p.bump(ctx, p.genKeyUser(org, user), p.genKeyCfg(org))
}

// InvalidateOrg — байгууллагын бүх хэрэглэгчийн cache (ж: owner солигдох, role manifest өөрчлөх).
func (p *Policy) InvalidateOrg(ctx context.Context, org string) error {
	p.users.invalidateOrg(org)
	p.orgs.invalidateOrg(org)
	return p.bump(ctx, p.genKeyOrg(org), p.genKeyCfg(org))
}

func (p *Policy) bump(ctx context.Context, keys ...string) error {
	if p.gens == nil {
		return nil
	}
	if err := p.gens.Bump(ctx, keys); err != nil {
		return fmt.Errorf("access: generation bump: %w", err)
	}
	return nil
}

// CacheSize — cache доторх хэрэглэгчийн entry-ийн тоо (metric/тест).
func (p *Policy) CacheSize() int { return p.users.size() }
