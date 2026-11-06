-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: VALIDATE (SHARE UPDATE EXCLUSIVE; бичилтийг хаахгүй).
ALTER TABLE organization.employee_extra_phones VALIDATE CONSTRAINT employee_extra_phones_org_id_nn;
ALTER TABLE organization.employee_extra_emails VALIDATE CONSTRAINT employee_extra_emails_org_id_nn;
ALTER TABLE organization.employee_extra_shifts VALIDATE CONSTRAINT employee_extra_shifts_org_id_nn;
ALTER TABLE public.sub_employee_skill VALIDATE CONSTRAINT sub_employee_skill_org_id_nn;
ALTER TABLE public.sub_employee_course VALIDATE CONSTRAINT sub_employee_course_org_id_nn;
ALTER TABLE public.sub_employee_education VALIDATE CONSTRAINT sub_employee_education_org_id_nn;
ALTER TABLE public.sub_employee_job_experience VALIDATE CONSTRAINT sub_employee_job_experience_org_id_nn;
