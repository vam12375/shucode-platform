import assert from "node:assert/strict";
import test from "node:test";
import {
  isTeamManager,
  resolveConsoleSection,
  routeAllowed,
  scopedPath,
} from "../src/permissions.ts";

const user = {
  id: "u1",
  email: "member@example.test",
  displayName: "Member",
  role: "user",
};
const membership = {
  team: { id: "team-a" },
  role: "admin",
  memberDisabledAt: null,
};

test("team management follows active membership rather than global admin", () => {
  assert.equal(isTeamManager({ ...user, role: "admin" }), false);
  assert.equal(isTeamManager(user, membership), true);
  assert.equal(isTeamManager(user, { ...membership, role: "owner" }), true);
  assert.equal(isTeamManager(user, { ...membership, role: "member" }), false);
  assert.equal(
    isTeamManager(user, { ...membership, memberDisabledAt: "2026-10-18" }),
    false,
  );
  assert.equal(
    isTeamManager(user, {
      ...membership,
      team: { ...membership.team, disabledAt: "2026-10-18" },
    }),
    false,
  );
  assert.equal(isTeamManager({ ...user, role: "root" }), true);
});

test("direct navigation cannot expose platform management to team admins", () => {
  for (const section of [
    "pricing",
    "models",
    "model-display",
    "providers",
    "ops",
    "users",
    "config",
    "releases",
    "ui",
    "teams",
  ]) {
    assert.equal(routeAllowed(section, false, true), false, section);
    assert.equal(routeAllowed(section, true, true), true, section);
  }
  for (const section of [
    "team",
    "members",
    "policy",
    "team-settings",
    "audit",
    "quota-requests",
  ]) {
    assert.equal(routeAllowed(section, false, false), false, section);
    assert.equal(routeAllowed(section, false, true), true, section);
  }
  for (const section of ["usage", "devices", "profile", "catalog", "invite"])
    assert.equal(routeAllowed(section, false, false), true);
});

test("scope covers usage, charts, CSV, audit and member lists without affecting authentication", () => {
  for (const path of [
    "/usage",
    "/usage/series",
    "/usage/export",
    "/audit",
    "/users",
    "/models",
    "/devices",
  ])
    assert.equal(scopedPath(path, "team-a"), `${path}?teamId=team-a`);
  assert.equal(
    scopedPath("/usage?from=2026-10-01", "team a"),
    "/usage?from=2026-10-01&teamId=team+a",
  );
  assert.equal(
    scopedPath("/usage?teamId=team-b", "team-a"),
    "/usage?teamId=team-b",
  );
  assert.equal(scopedPath("/auth/me", "team-a"), "/auth/me");
  assert.equal(scopedPath("/model-display", "team-a"), "/model-display");
  assert.equal(
    scopedPath("/teams/team-a/policy", "team-a"),
    "/teams/team-a/policy",
  );
  assert.equal(scopedPath("/usage", ""), "/usage");
});

test("legacy teams entry routes members to personal usage and managers to the workspace", () => {
  assert.equal(resolveConsoleSection("teams", false, false), "usage");
  assert.equal(resolveConsoleSection("teams", false, true), "team");
  assert.equal(resolveConsoleSection("teams", true, true), "teams");
  assert.equal(resolveConsoleSection("invite", false, false), "invite");
});
