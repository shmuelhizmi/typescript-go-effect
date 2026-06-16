// .etsx: JSX + EffectScript in one file (SPEC §12)
import * as Effect from "effect/Effect";
declare const useEffectRunner: () => (e: any) => void;
declare const purchase: (item: string) => any;
declare namespace JSX {
    interface IntrinsicElements {
        button: any;
    }
}
export function BuyButton({ item }: {
    item: string;
}) {
    const run = useEffectRunner();
    return (<button onClick={() => run(Effect.gen(function* () {
            const receipt = yield* purchase(item);
            yield* Effect.log(`charged ${receipt.amount}`);
        }))}>
      Buy now
    </button>);
}
