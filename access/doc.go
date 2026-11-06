// Package access нь модуль → role → capability загварт суурилсан эрхийн
// (authorization) цөм юм. main-ийн `modulepermission` багцыг орлоно.
//
// Бүтэц:
//
//   - [Registry] — модулиудын [Manifest] (role, capability, анхдагч role) бүртгэл.
//     Global биш; апп өөрөө үүсгэж, шалгагдсаны дараа [Registry.Freeze] хийгдэнэ.
//   - [Grants] — нэг (org, user)-ийн эрх: owner, модуль бүрийн role-ууд, capability
//     allow/deny. [Source] интерфейсээр апп өөрийн хадгалалтаас уншуулна
//     ([GormSource], [MemorySource], [Merge]).
//   - [Policy] — Registry + Source-ийг санах ойд compile хийж, TTL cache,
//     singleflight, [Policy.Invalidate], (сонголтоор) олон instance-ийн generation
//     invalidation-тэй шийдвэр гаргана.
//   - HTTP: [Policy.Require], [Policy.Can], [Policy.CrudCan], [Policy.Me].
//
// Шийдвэрийн дүрэм (fail-closed):
//
//  1. Хэрэглэгч/байгууллага тодорхойгүй, модуль/capability manifest-д байхгүй → татгалзана.
//  2. Source алдаа → татгалзаж, лог бичнэ (эрх олгохгүй).
//  3. Owner → manifest-ийн БҮХ capability (тохируулж болно, [OwnerPolicy]).
//  4. Бусад: (role-уудын анхдагч ∪ explicit allow) ∖ deny. Deny үргэлж allow-г дарна.
//  5. Шилжилтийн горим ([WithTransition]) зөвхөн тусгайлан асаасан модульд: тохируулаагүй
//     модуль нээлттэй (одоогийн хуучин зан төлөв). Анхдагч нь strict.
//
// Identity нь ЗӨВХӨН JWT-ээс ([tenant.OrgID], [auth.User]); header/query-д итгэхгүй.
package access
