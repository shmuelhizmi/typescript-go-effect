import { Effect } from "effect";

declare const useEffectRunner: () => (e: any) => void;
declare const purchase: (item: string) => any;
declare namespace JSX { interface IntrinsicElements { button: any } }

export function BuyButton({ item }: { item: string }) {
    const run = useEffectRunner();
    return (
        <button onClick={() => run(Effect.gen(function* () {
            const receipt = yield* purchase(item);
            yield* Effect.log(`charged ${receipt.amount}`);
        }))}>
            Buy now
        </button>
    );
}

// The Effect transform desugars effect constructs; JSX is left intact here and
// lowered by the regular JSX transform according to the `jsx` compiler option.
