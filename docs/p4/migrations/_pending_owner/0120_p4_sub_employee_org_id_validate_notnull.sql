-- PENDING OWNER (public.sub_employee_skill/course/education/job_experience).
-- БАТАЛСАН СЕМАНТИК: employee_id = public.users.id (Lambda form 1587-1590 model=users, SubForm parent=employee_id; prod: 26/26 мөр users-тэй таарна, organization.employee-тэй 0/26).
-- Эдгээр нь хэрэглэгчийн ПРОФАЙЛ (хувийн) өгөгдөл: хэрэглэгч олон org-ийн гишүүн (prod: 26 мөрийн 100% нь олон org-ийн гишүүн), тиймээс "нэг org" гэж байхгүй.
-- W0-д org_id нь users.org_id (home org)-оор best-effort дүүргэгдсэн, NOT NULL/CHECK/RLS-гүй.
-- Owner шийдвэр: (A) tenant-ийн өгөгдөл биш - org_id багана хэрэггүй (хас/үл тоо), (B) home org-оор tenant болгох -> ЭНЭ файл, (C) org бүрт хуулбарлах.
-- (B) сонговол: users.org_id IS NULL мөр (role=4) байхгүй гэдгийг шалгаад:
DO $$
DECLARE n bigint;
BEGIN
  SELECT (SELECT count(*) FROM public.sub_employee_skill WHERE org_id IS NULL) + (SELECT count(*) FROM public.sub_employee_course WHERE org_id IS NULL)
       + (SELECT count(*) FROM public.sub_employee_education WHERE org_id IS NULL) + (SELECT count(*) FROM public.sub_employee_job_experience WHERE org_id IS NULL) INTO n;
  IF n > 0 THEN RAISE EXCEPTION 'P4 gate: sub_employee_* org_id NULL=% (owner шийдээгүй)', n; END IF;
END $$;
ALTER TABLE public.sub_employee_skill ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_course ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_education ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_job_experience ALTER COLUMN org_id SET NOT NULL;
