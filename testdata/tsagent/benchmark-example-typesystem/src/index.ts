export type {
    BillingCatalog,
    BillingHandlers,
    NormalizedEverything,
    RepoCatalog,
    RepoHandlers,
    RequestSamples,
    ResponseSamples,
    UserCatalog,
    UserHandlers,
} from "./instances";
export {
    auditSlot,
    describeResource,
    invoiceSlot,
    normalizedUserSlot,
    repoSlot,
    sampleAudit,
    sampleInvoice,
    sampleRepo,
    sampleTeam,
    sampleUser,
    userSlot,
} from "./instances";
export type {
    AllEnvelopes,
    AllHandlers,
    AllRoutesCatalog,
    DeepNormalize,
    EnvelopeMatrix,
    HandlerMap,
    RouteCatalog,
    RouteKey,
    RouteRequest,
    RouteResponse,
} from "./hotspot";
export { TypedSlot } from "./hotspot";

import { describeResource, repoSlot, userSlot } from "./instances";

export function runFixture(): string {
    const user = userSlot.get();
    const repo = repoSlot.map((value) => value.slug).get();
    return `${user.name}:${repo}:${describeResource("repo")}`;
}
