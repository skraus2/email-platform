import "server-only";
import { cache } from "react";
import {
  adminFetch,
  adminFetchAll,
  type Project,
  type TemplateDetail,
  type TemplateVersion,
} from "./admin-api";

/** Projects loaded for names and selection; more are reported as "more". */
export const PROJECT_CAP = 500;

/*
 * Loaders shared by a page and its generateMetadata within one request
 * (React cache), so the title costs no extra admin API call. Callers pass
 * validated ids.
 */

export const loadProjects = cache(() =>
  adminFetchAll<Project>("/admin/v1/projects", PROJECT_CAP),
);

export const loadTemplate = cache((id: string) =>
  adminFetch<TemplateDetail>(`/admin/v1/templates/${encodeURIComponent(id)}`),
);

export const loadTemplateVersion = cache((id: string, version: number) =>
  adminFetch<TemplateVersion>(
    `/admin/v1/templates/${encodeURIComponent(id)}/versions/${version}`,
  ),
);

/** The project of an id from the project list (there is no single-project route). */
export async function findProject(id: string): Promise<Project | undefined> {
  const { rows } = await loadProjects();
  return rows.find((p) => p.id === id);
}
