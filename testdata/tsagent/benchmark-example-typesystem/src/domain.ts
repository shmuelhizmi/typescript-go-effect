export type Resource = "user" | "team" | "repo" | "invoice" | "audit";
export type Verb = "get" | "list" | "create" | "update" | "delete";
export type Region = "us" | "eu" | "apac";
export type Mode = "sync" | "async";

export interface User {
    id: string;
    name: string;
    email: string;
    flags: readonly string[];
}

export interface Team {
    id: string;
    name: string;
    members: readonly User[];
}

export interface Repo {
    id: string;
    slug: string;
    owner: Team;
    topics: readonly string[];
}

export interface Invoice {
    id: string;
    cents: number;
    paid: boolean;
    issuedAt: Date;
}

export interface AuditEntry {
    id: string;
    actor: User;
    resource: Resource;
    action: Verb;
    at: Date;
}

export interface EntityMap {
    user: User;
    team: Team;
    repo: Repo;
    invoice: Invoice;
    audit: AuditEntry;
}

export type Entity = EntityMap[keyof EntityMap];

export interface TraceInfo {
    id: string;
    region: Region;
    mode: Mode;
}

export interface Envelope<T, Kind extends string = string> {
    kind: Kind;
    payload: T;
    trace: TraceInfo;
}
