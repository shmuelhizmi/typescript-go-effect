import type {
    AuditEntry,
    Invoice,
    Region,
    Repo,
    Resource,
    Team,
    User,
    Verb,
} from "./domain";
import {
    TypedSlot,
    type AllRoutesCatalog,
    type DeepNormalize,
    type HandlerMap,
    type RouteCatalog,
    type RouteKey,
    type RouteRequest,
    type RouteResponse,
} from "./hotspot";

export type UserRouteKeys = RouteKey<"user", Verb, Region>;
export type RepoReadRouteKeys = RouteKey<"repo" | "team", "get" | "list" | "update", Region>;
export type BillingRouteKeys = RouteKey<"invoice" | "audit", Verb, "us" | "eu">;

export type UserHandlers = HandlerMap<UserRouteKeys>;
export type RepoHandlers = HandlerMap<RepoReadRouteKeys>;
export type BillingHandlers = HandlerMap<BillingRouteKeys>;

export type UserCatalog = RouteCatalog<UserRouteKeys>;
export type RepoCatalog = RouteCatalog<RepoReadRouteKeys>;
export type BillingCatalog = RouteCatalog<BillingRouteKeys>;
export type NormalizedEverything = DeepNormalize<AllRoutesCatalog>;

export type RequestSamples =
    | RouteRequest<RouteKey<"user", "get", "us", "sync">>
    | RouteRequest<RouteKey<"repo", "list", "eu", "async">>
    | RouteRequest<RouteKey<"invoice", "update", "apac", "sync">>;

export type ResponseSamples =
    | RouteResponse<RouteKey<"user", "get", "us", "sync">>
    | RouteResponse<RouteKey<"repo", "list", "eu", "async">>
    | RouteResponse<RouteKey<"audit", "delete", "apac", "async">>;

export const sampleUser: User = {
    id: "u1",
    name: "Ada",
    email: "ada@example.test",
    flags: ["admin", "beta"],
};

export const sampleTeam: Team = {
    id: "t1",
    name: "Compiler",
    members: [sampleUser],
};

export const sampleRepo: Repo = {
    id: "r1",
    slug: "agent-tools",
    owner: sampleTeam,
    topics: ["typescript", "perf"],
};

export const sampleInvoice: Invoice = {
    id: "i1",
    cents: 12500,
    paid: false,
    issuedAt: new Date(0),
};

export const sampleAudit: AuditEntry = {
    id: "a1",
    actor: sampleUser,
    resource: "repo",
    action: "update",
    at: new Date(0),
};

export const userSlot = new TypedSlot<User, "user">("user", sampleUser);
export const repoSlot = new TypedSlot<Repo, "repo">("repo", sampleRepo);
export const invoiceSlot = new TypedSlot<Invoice, "invoice">("invoice", sampleInvoice);
export const auditSlot = new TypedSlot<AuditEntry, "audit">("audit", sampleAudit);

export const normalizedUserSlot = new TypedSlot<DeepNormalize<User>, "normalized-user">(
    "normalized-user",
    {
        id: sampleUser.id,
        name: sampleUser.name,
        email: sampleUser.email,
        flags: sampleUser.flags,
    },
);

export function describeResource(resource: Resource): string {
    return `${resource}:tracked`;
}
