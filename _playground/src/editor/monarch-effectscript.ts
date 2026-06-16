// Monarch grammar for EffectScript: Monaco's official TypeScript tokenizer
// extended with the EffectScript contextual keywords and operators. Monarch
// cannot model context-sensitivity, so the keywords always highlight as
// keywords; LSP semantic tokens overlay the precise classification.
import type { languages } from "monaco-editor";
import { conf as tsConf, language as tsLanguage } from "monaco-editor/esm/vs/basic-languages/typescript/typescript.js";

const ETS_KEYWORDS = [
    "effect",
    "atomic",
    "raise",
    "service",
    "layer",
    "fork",
    "par",
    "race",
    "defer",
    "release",
    "raises",
    "requires",
    "provide",
    "scoped",
    "join",
    "match",
    "tagged",
    "schema",
];

export const effectscriptConf: languages.LanguageConfiguration = tsConf;

export const effectscriptLanguage: languages.IMonarchLanguage = {
    ...tsLanguage,
    keywords: [...tsLanguage.keywords, ...ETS_KEYWORDS],
    // "<-"/"|>" are matched by the existing @symbols regex; listing them in
    // operators classifies them as "operator" instead of "delimiter".
    operators: [...tsLanguage.operators, "<-", "|>"],
};
