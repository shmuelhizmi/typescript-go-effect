import type {
    EntityMap,
    Envelope,
    Mode,
    Region,
    Resource,
    Verb,
} from "./domain";

export type RouteKey<
    R extends Resource = Resource,
    V extends Verb = Verb,
    G extends Region = Region,
    M extends Mode = Mode,
> = `${G}.${M}.${R}.${V}`;

export type RouteParts<K extends string> =
    K extends `${infer G}.${infer M}.${infer R}.${infer V}`
        ? { region: G; mode: M; resource: R; verb: V }
        : never;

export type ExtractResource<K extends string> =
    K extends `${Region}.${Mode}.${infer R extends Resource}.${Verb}` ? R : never;

export type ExtractVerb<K extends string> =
    K extends `${Region}.${Mode}.${Resource}.${infer V extends Verb}` ? V : never;

export type ExtractMode<K extends string> =
    K extends `${Region}.${infer M extends Mode}.${Resource}.${Verb}` ? M : never;

export type MaybePromise<T> = T | Promise<T>;

export type AwaitedValue<T> =
    T extends PromiseLike<infer Inner> ? AwaitedValue<Inner> : T;

export type ApiResult<T> =
    | { ok: true; value: T; meta: { cached: boolean } }
    | { ok: false; error: { code: string; message: string } };

export type EntityFor<K extends RouteKey> = EntityMap[ExtractResource<K>];

export type RouteRequest<K extends RouteKey> =
    ExtractVerb<K> extends "get"
        ? { id: string; trace: RouteParts<K> }
        : ExtractVerb<K> extends "list"
            ? { cursor?: string; limit: number; trace: RouteParts<K> }
            : ExtractVerb<K> extends "create"
                ? { body: EntityFor<K>; trace: RouteParts<K> }
                : ExtractVerb<K> extends "update"
                    ? { id: string; patch: Partial<EntityFor<K>>; trace: RouteParts<K> }
                    : { id: string; hard?: boolean; trace: RouteParts<K> };

export type RouteResponse<K extends RouteKey> =
    ExtractVerb<K> extends "list"
        ? ApiResult<readonly EntityFor<K>[]>
        : ExtractVerb<K> extends "delete"
            ? ApiResult<{ deleted: true; id: string }>
            : ApiResult<EntityFor<K>>;

export type Handler<K extends RouteKey> = (
    request: RouteRequest<K>,
) => MaybePromise<RouteResponse<K>>;

export type HandlerMap<Keys extends RouteKey> = {
    readonly [K in Keys as `handle:${K}`]: Handler<K>;
};

export type JsonLeaf = string | number | boolean | null;

export type DeepNormalize<T> =
    T extends Date
        ? string
        : T extends JsonLeaf
            ? T
            : T extends readonly (infer Item)[]
                ? readonly DeepNormalize<Item>[]
                : T extends (...args: readonly never[]) => unknown
                    ? never
                    : T extends object
                        ? { readonly [K in keyof T as K extends symbol ? never : K]: DeepNormalize<T[K]> }
                        : T;

export type MutableFlags<T> = {
    -readonly [K in keyof T as K extends string ? `canSet:${K}` : never]: boolean;
};

export type EnvelopeMatrix<Keys extends RouteKey> = {
    readonly [K in Keys]: Envelope<
        DeepNormalize<RouteResponse<K>>,
        `route:${K}:${ExtractMode<K>}`
    >;
};

export type RouteCatalog<Keys extends RouteKey = RouteKey> = {
    readonly [K in Keys]: RouteParts<K> & {
        request: DeepNormalize<RouteRequest<K>>;
        response: DeepNormalize<AwaitedValue<RouteResponse<K>>>;
        flags: MutableFlags<RouteRequest<K>>;
    };
};

export type AllHandlers = HandlerMap<RouteKey>;
export type AllEnvelopes = EnvelopeMatrix<RouteKey>;
export type AllRoutesCatalog = RouteCatalog<RouteKey>;

export class TypedSlot<T, Name extends string> {
    constructor(
        readonly name: Name,
        private value: T,
    ) {}

    get(): T {
        return this.value;
    }

    set(value: T): void {
        this.value = value;
    }

    map<U>(fn: (value: T) => U): TypedSlot<U, `${Name}:mapped`> {
        return new TypedSlot(`${this.name}:mapped` as `${Name}:mapped`, fn(this.value));
    }
}
